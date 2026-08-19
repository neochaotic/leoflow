package logs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"time"
)

// ErrObjectNotFound reports that an object-store sink holds no stored log for a
// Ref. Backends translate their own not-found signal (S3 NoSuchKey, GCS
// ErrObjectNotExist, a missing in-memory entry) into this sentinel so the read
// path maps it uniformly.
var ErrObjectNotFound = errors.New("log object not found")

// ObjectStore is the minimal object-store API the ObjectSink needs: write a
// whole object under a key, and read one back. It is deliberately tiny so the
// sink is testable against an in-memory fake, and so any backend can satisfy it
// with its own native SDK — the S3 backend (AWS S3, MinIO, Ceph RGW via
// aws-sdk-go-v2) and the GCS backend (Google Cloud Storage via its native SDK).
// Each provider keeps its own keyless auth path this way. Implementations target
// a single, pre-configured bucket.
type ObjectStore interface {
	Put(ctx context.Context, key string, r io.Reader) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// Pruner is implemented by sinks that reclaim their own expired storage: the
// disk sink deletes old files. An object-store sink deliberately does not
// implement it — bucket lifecycle policy owns retention there — so the janitor
// skips pruning for sinks that manage their own lifecycle.
type Pruner interface {
	Prune(now time.Time, retention time.Duration) error
}

// ObjectSink stores each task attempt as a single object in an ObjectStore,
// keyed the same way the disk sink lays out files
// ({prefix}/{tenant}/{dag}/{run}/{task}/{try}.log). Object stores have no
// append, so a writer buffers the attempt's events and writes one object on
// Close; the live-tail path (a separate Tailer) still streams lines as they
// arrive. It is opt-in — the disk sink stays the default (see NewDurableSink).
type ObjectSink struct {
	ctx    context.Context
	store  ObjectStore
	prefix string
}

// NewObjectSink builds an ObjectSink writing to store under an optional key
// prefix. ctx bounds the store operations issued by the writers and readers the
// sink hands out (the Sink interface is context-free by design); pass the
// server's lifecycle context.
func NewObjectSink(ctx context.Context, store ObjectStore, prefix string) *ObjectSink {
	return &ObjectSink{ctx: ctx, store: store, prefix: prefix}
}

// key maps a Ref to its object key, mirroring DiskSink's on-disk layout so an
// operator can reason about both the same way. path.Join (not filepath.Join)
// keeps forward slashes on every OS, since object keys are not filesystem paths.
func (o *ObjectSink) key(ref Ref) string {
	return path.Join(o.prefix, ref.TenantID, ref.DagID, ref.RunID, ref.TaskID, fmt.Sprintf("%d.log", ref.TryNumber))
}

// Open validates the ref and returns a writer that buffers events and flushes
// them as one object on Close.
func (o *ObjectSink) Open(ref Ref) (LogWriter, error) {
	if err := ref.validate(); err != nil {
		return nil, err
	}
	return &objectWriter{ctx: o.ctx, store: o.store, key: o.key(ref)}, nil
}

// Read fetches the stored object for the ref. A missing object surfaces as
// ErrObjectNotFound.
func (o *ObjectSink) Read(ref Ref) (io.ReadCloser, error) {
	if err := ref.validate(); err != nil {
		return nil, err
	}
	rc, err := o.store.Get(o.ctx, o.key(ref))
	if err != nil {
		return nil, fmt.Errorf("reading log object: %w", err)
	}
	return rc, nil
}

// maxBufferedAttemptBytes caps how much of a single task attempt the object sink
// buffers in the control plane. Object stores have no append, so the whole
// attempt is held in RAM until Close; without a cap one chatty task could OOM the
// shared control plane (unlike the disk sink, which flushes incrementally). This
// bound is far above any sane task log yet keeps the blast radius of a runaway
// task to its own attempt.
// var (not const) so tests can lower it without buffering 128 MiB.
var maxBufferedAttemptBytes = 128 << 20 // 128 MiB

// objectWriter accumulates a task attempt's events in memory and writes them as
// a single object on Close. Object stores do not support append, so the whole
// attempt is one Put; memory use is bounded by maxBufferedAttemptBytes.
type objectWriter struct {
	ctx   context.Context
	store ObjectStore
	key   string
	buf   bytes.Buffer
}

// WriteEvent appends an event to the in-memory buffer as a JSON line, matching
// the JSONL format the disk sink writes and the UI reader decodes. It fails
// loudly once the attempt exceeds maxBufferedAttemptBytes rather than growing the
// control plane's memory without bound; whatever was buffered before the cap is
// still flushed on Close.
func (w *objectWriter) WriteEvent(ev Event) error {
	line := EncodeLine(ev) + "\n"
	if w.buf.Len()+len(line) > maxBufferedAttemptBytes {
		return fmt.Errorf("task attempt log exceeds the %d-byte object-sink buffer cap; not buffering further lines", maxBufferedAttemptBytes)
	}
	if _, err := w.buf.WriteString(line); err != nil {
		return fmt.Errorf("buffering log line: %w", err)
	}
	return nil
}

// Close writes the buffered attempt to the object store as one object.
func (w *objectWriter) Close() error {
	if err := w.store.Put(w.ctx, w.key, bytes.NewReader(w.buf.Bytes())); err != nil {
		return fmt.Errorf("writing log object: %w", err)
	}
	return nil
}

// NewDurableSink selects the durable log sink from configuration. The default —
// an empty or "disk" backend — returns a DiskSink rooted at dir, so Lite and
// every deployment that does not opt in keep the exact on-disk behavior. The
// "s3" and "gcs" backends return an ObjectSink over store (which the caller
// builds with the matching native SDK from the configured bucket/credentials)
// and require a non-nil store. An unknown backend is rejected rather than
// silently falling back.
func NewDurableSink(ctx context.Context, backend, dir string, store ObjectStore, prefix string) (Sink, error) {
	switch backend {
	case "", "disk":
		return NewDiskSink(dir), nil
	case "s3", "gcs":
		if store == nil {
			return nil, fmt.Errorf("%s log backend requires an object store", backend)
		}
		return NewObjectSink(ctx, store, prefix), nil
	default:
		return nil, fmt.Errorf("unknown log backend %q (want \"disk\", \"s3\" or \"gcs\")", backend)
	}
}
