// Package logs ships task logs from the agent to durable storage and serves
// them back via the API, so logs remain available after the task pod is gone.
package logs

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// flushThreshold is the buffered-writer size; a full buffer flushes to storage,
// bounding how much is lost on a crash and how stale a live reader can be.
const flushThreshold = 1 << 20 // 1 MB

// Event is one structured log line: when it was emitted, its severity level and
// source stream (stdout/stderr), and the message text. Logs are stored as JSONL
// (one Event per line) so the UI's drill-down viewer can color by real level
// instead of guessing.
type Event struct {
	Time    time.Time `json:"ts"`
	Level   string    `json:"level"`
	Stream  string    `json:"stream"`
	Message string    `json:"msg"`
}

// DecodeLine parses a stored log line into an Event. Lines that are not JSON
// (legacy plain-text logs written before structured storage) decode as a
// stdout message with a level inferred from the text, so the reader serves both
// formats with sensible coloring.
func DecodeLine(line string) Event {
	if strings.HasPrefix(line, "{") {
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err == nil {
			if ev.Level == "" {
				ev.Level = "info"
			}
			if ev.Stream == "" {
				ev.Stream = "stdout"
			}
			return ev
		}
	}
	return Event{Level: inferLevel(line), Stream: "stdout", Message: line}
}

// inferLevel guesses a level from a legacy plain line's text, defaulting to
// info when nothing in the text indicates a severity.
func inferLevel(line string) string { return RefineLevel(line, "info") }

// RefineLevel returns the severity indicated by a log line's own text when it
// carries a clear level token, otherwise the supplied fallback. It lets the line
// content correct a level that was derived only from the source stream — so an
// "ERROR …" line printed to stdout is colored error (not info), and an "INFO …"
// line written to stderr is colored info (not error). When the text gives no
// signal, the stream-derived fallback stands, so behavior is unchanged for plain
// output. The returned values match the Event.Level vocabulary the UI colors by
// (error/warning/info/debug); callers control the contract, this only sharpens
// the level value.
func RefineLevel(line, fallback string) string {
	if lvl, ok := levelFromContent(line); ok {
		return lvl
	}
	return fallback
}

// levelFromContent reports the severity a line's text indicates, if any. Tokens
// are matched uppercase (standard logger output: Python logging, structured
// loggers) to avoid false positives from ordinary lowercase prose. error is
// checked first so "ERROR" wins over a stray "INFO" later in the same line.
func levelFromContent(line string) (string, bool) {
	switch {
	case containsAny(line, "ERROR", "CRITICAL", "FATAL", "Traceback", "Exception"):
		return "error", true
	case containsAny(line, "WARNING", "WARN"):
		return "warning", true
	case containsAny(line, "DEBUG"):
		return "debug", true
	case containsAny(line, "INFO", "NOTICE"):
		return "info", true
	default:
		return "", false
	}
}

// containsAny reports whether s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// Ref identifies a task instance's log stream and maps to its storage location.
type Ref struct {
	TenantID  string
	DagID     string
	RunID     string
	TaskID    string
	TryNumber int
}

// LogWriter appends structured log events for one task attempt and flushes on
// Close.
type LogWriter interface {
	WriteEvent(ev Event) error
	Close() error
}

// Sink stores and retrieves task logs.
type Sink interface {
	Open(ref Ref) (LogWriter, error)
	Read(ref Ref) (io.ReadCloser, error)
}

// DiskSink writes logs to ${root}/{tenant}/{dag}/{run}/{task}/{try}.log.
type DiskSink struct {
	root string
}

// NewDiskSink builds a DiskSink rooted at dir.
func NewDiskSink(dir string) *DiskSink { return &DiskSink{root: dir} }

func (d *DiskSink) path(ref Ref) string {
	return filepath.Join(d.root, ref.TenantID, ref.DagID, ref.RunID, ref.TaskID, fmt.Sprintf("%d.log", ref.TryNumber))
}

// Open creates the log file (appending if it exists) and returns a buffered writer.
func (d *DiskSink) Open(ref Ref) (LogWriter, error) {
	p := d.path(ref)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return nil, fmt.Errorf("creating log directory: %w", err)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640) //nolint:gosec // path is built from validated identity fields
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}
	return &diskWriter{f: f, buf: bufio.NewWriterSize(f, flushThreshold)}, nil
}

// Prune deletes log files whose last modification is older than retention,
// reclaiming space for completed runs. A missing root is not an error.
func (d *DiskSink) Prune(now time.Time, retention time.Duration) error {
	cutoff := now.Add(-retention)
	err := filepath.WalkDir(d.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".log" {
			return nil
		}
		info, ierr := entry.Info()
		if ierr != nil {
			return ierr
		}
		if info.ModTime().Before(cutoff) {
			//nolint:gosec // G122: the log root is server-owned, not an untrusted symlink tree.
			if rerr := os.Remove(path); rerr != nil {
				return rerr
			}
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Read opens the log file for reading.
func (d *DiskSink) Read(ref Ref) (io.ReadCloser, error) {
	f, err := os.Open(d.path(ref)) //nolint:gosec // path is built from validated identity fields
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}
	return f, nil
}

// diskWriter buffers line writes and flushes them to the file.
type diskWriter struct {
	f   *os.File
	buf *bufio.Writer
}

// WriteEvent appends an event as a JSON line; the buffer flushes automatically
// at flushThreshold.
func (w *diskWriter) WriteEvent(ev Event) error {
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	encoded, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encoding log event: %w", err)
	}
	if _, err := w.buf.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("writing log line: %w", err)
	}
	return nil
}

// Close flushes any buffered lines and closes the file.
func (w *diskWriter) Close() error {
	return errors.Join(w.buf.Flush(), w.f.Close())
}
