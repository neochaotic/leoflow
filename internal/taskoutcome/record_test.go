package taskoutcome

import (
	"strings"
	"testing"
	"time"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	at := time.Date(2026, 8, 13, 22, 6, 3, 0, time.UTC)
	cases := []struct {
		name string
		rec  Record
	}{
		{"success", Succeeded()},
		{"failed", FailedWith(1)},
		{"failed-zero-exit", FailedWith(0)},
		{"reschedule", RescheduledAt(at)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			enc, err := c.rec.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			got, ok := Decode(enc)
			if !ok {
				t.Fatalf("Decode(%q) not ok", enc)
			}
			if got.Outcome != c.rec.Outcome {
				t.Errorf("outcome = %q, want %q", got.Outcome, c.rec.Outcome)
			}
		})
	}
}

func TestDecodeExitCode(t *testing.T) {
	enc, _ := FailedWith(42).Encode()
	got, ok := Decode(enc)
	if !ok {
		t.Fatal("not ok")
	}
	if got.ExitCode == nil || *got.ExitCode != 42 {
		t.Errorf("exit_code = %v, want 42", got.ExitCode)
	}
	// A success carries no exit code.
	sEnc, _ := Succeeded().Encode()
	s, _ := Decode(sEnc)
	if s.ExitCode != nil {
		t.Errorf("success exit_code = %v, want nil", *s.ExitCode)
	}
}

func TestDecodeRescheduleAt(t *testing.T) {
	at := time.Date(2026, 8, 13, 22, 30, 0, 0, time.UTC)
	enc, _ := RescheduledAt(at).Encode()
	got, ok := Decode(enc)
	if !ok {
		t.Fatal("not ok")
	}
	when, ok := got.At()
	if !ok {
		t.Fatal("At() not ok for a reschedule record")
	}
	if !when.Equal(at) {
		t.Errorf("At() = %v, want %v", when, at)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	// The termination message may be empty, a truncated log tail (if an operator
	// ever set FallbackToLogsOnError), or an old-agent's arbitrary text. Decode
	// must reject all of it so the reconciler falls back to pod phase.
	bad := []string{
		"",
		"   ",
		"not json at all",
		`{"v":1}`,                        // no outcome
		`{"v":2,"outcome":"success"}`,    // unknown version
		`{"v":1,"outcome":"exploded"}`,   // unknown outcome
		`{"v":1,"outcome":"reschedule"}`, // reschedule without a time
		`{"v":1,"outcome":"reschedule","reschedule_at":"soon"}`, // unparseable time
		"Traceback (most recent call last):\n  File ...",        // a log tail
	}
	for _, b := range bad {
		if _, ok := Decode(b); ok {
			t.Errorf("Decode(%q) = ok, want rejected", b)
		}
	}
}

func TestEncodeStaysUnderCap(t *testing.T) {
	// Kubernetes caps the termination message; the record must be a few bytes.
	enc, err := FailedWith(255).Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(enc) > 256 {
		t.Errorf("encoded record is %d bytes; expected well under the K8s cap", len(enc))
	}
	if !strings.Contains(enc, `"v":1`) {
		t.Errorf("encoded record must carry the version tag: %q", enc)
	}
}
