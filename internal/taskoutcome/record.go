// Package taskoutcome defines the durable task-outcome record the agent writes to
// its container termination message before delivering the report, and the
// reconciler reads back as the source of truth over pod phase (ADR 0052).
//
// The record decouples a task's true result from the delivery of that result: a
// pod killed mid-report still leaves the outcome behind, so a task that succeeded
// but lost its report is not misread as a failure. The format is a compact,
// versioned JSON document kept far under the Kubernetes termination-message cap;
// it carries the outcome only, never logs or return values.
package taskoutcome

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Version is the record schema version. The reader rejects any other version so
// the format can evolve without a stale reader misinterpreting a newer record.
const Version = 1

// maxRecordBytes bounds an encoded record. The real Kubernetes cap is ~4 KiB; a
// record is a few dozen bytes, so a value far above any real record but well under
// the cap catches a programming error (a giant field) without false positives.
const maxRecordBytes = 1024

// Outcome is a task's true result, independent of report delivery.
type Outcome string

const (
	// Success means the user task completed successfully.
	Success Outcome = "success"
	// Failed means the user task failed; ExitCode carries the process exit code.
	Failed Outcome = "failed"
	// Reschedule means a reschedule-mode sensor poked not-ready; RescheduleAt
	// carries the next-poke time. Never settled as a failure by the reconciler.
	Reschedule Outcome = "reschedule"
)

// Record is the durable outcome document. Only the fields relevant to an outcome
// are populated: ExitCode for Failed, RescheduleAt for Reschedule.
type Record struct {
	V            int     `json:"v"`
	Outcome      Outcome `json:"outcome"`
	ExitCode     *int32  `json:"exit_code,omitempty"`
	RescheduleAt string  `json:"reschedule_at,omitempty"`
}

// Succeeded returns a success record.
func Succeeded() Record { return Record{V: Version, Outcome: Success} }

// FailedWith returns a failure record carrying the user process exit code.
func FailedWith(exitCode int32) Record {
	return Record{V: Version, Outcome: Failed, ExitCode: &exitCode}
}

// RescheduledAt returns a reschedule record carrying the next-poke time. The time
// is stored in RFC3339Nano (UTC) so the reconciler can settle up_for_reschedule
// with the real poke time rather than inventing one.
func RescheduledAt(when time.Time) Record {
	return Record{V: Version, Outcome: Reschedule, RescheduleAt: when.UTC().Format(time.RFC3339Nano)}
}

// Encode renders the record as the string to write to the termination message.
// It errors on an unexpectedly large result, which would signal a programming
// error rather than a real outcome.
func (r Record) Encode() (string, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	if len(b) > maxRecordBytes {
		return "", errors.New("taskoutcome: encoded record exceeds size cap")
	}
	return string(b), nil
}

// At parses the reschedule time; ok is false unless the record is a reschedule
// carrying a parseable RFC3339 time.
func (r Record) At() (time.Time, bool) {
	if r.Outcome != Reschedule || r.RescheduleAt == "" {
		return time.Time{}, false
	}
	when, err := time.Parse(time.RFC3339Nano, r.RescheduleAt)
	if err != nil {
		return time.Time{}, false
	}
	return when, true
}

// Decode parses a termination message into a Record. ok is false for anything
// that is not a valid, current-version record with a known outcome — an empty
// message, arbitrary text, a log tail, an unknown version, or a reschedule with no
// parseable time — so the caller falls back to pod phase. Decode never errors; a
// malformed message is simply "no record".
func Decode(msg string) (Record, bool) {
	msg = strings.TrimSpace(msg)
	if msg == "" || msg[0] != '{' {
		return Record{}, false
	}
	var r Record
	if err := json.Unmarshal([]byte(msg), &r); err != nil {
		return Record{}, false
	}
	if r.V != Version {
		return Record{}, false
	}
	switch r.Outcome {
	case Success, Failed:
		return r, true
	case Reschedule:
		if _, ok := r.At(); !ok {
			return Record{}, false
		}
		return r, true
	default:
		return Record{}, false
	}
}
