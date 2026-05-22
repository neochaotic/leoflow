package xcom

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

type fakeBackend struct {
	entries map[string]Entry
	ttl     time.Duration
}

func newFakeBackend() *fakeBackend { return &fakeBackend{entries: map[string]Entry{}} }

func (b *fakeBackend) Push(_ context.Context, key string, e Entry, ttl time.Duration) error {
	b.entries[key] = e
	b.ttl = ttl
	return nil
}

func (b *fakeBackend) Fetch(_ context.Context, key string) (Entry, error) {
	e, ok := b.entries[key]
	if !ok {
		return Entry{}, ErrNotFound
	}
	return e, nil
}

func (b *fakeBackend) Delete(_ context.Context, key string) error {
	delete(b.entries, key)
	return nil
}

func (b *fakeBackend) List(_ context.Context, _ string) ([]string, error) { return nil, nil }

type fakeIndex struct{ recorded []IndexEntry }

func (i *fakeIndex) RecordXCom(_ context.Context, e IndexEntry) error {
	i.recorded = append(i.recorded, e)
	return nil
}

func testKey() Key {
	return Key{TenantID: "acme", DagID: "etl", RunID: "run-1", TaskID: "extract", Name: "return_value"}
}

func newService(b Backend, idx Index) *Service {
	s := NewService(b, idx, 7*24*time.Hour)
	s.now = func() time.Time { return time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC) }
	return s
}

func TestKeyString(t *testing.T) {
	if got := testKey().String(); got != "xcom:acme:etl:run-1:extract:return_value" {
		t.Errorf("Key.String() = %q", got)
	}
}

func TestPushFetchRoundTrip(t *testing.T) {
	backend := newFakeBackend()
	idx := &fakeIndex{}
	s := newService(backend, idx)

	if err := s.Push(context.Background(), testKey(), []byte(`{"rows":100}`), "application/json", nil); err != nil {
		t.Fatalf("Push: %v", err)
	}
	got, err := s.Fetch(context.Background(), testKey())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !bytes.Equal(got.Value, []byte(`{"rows":100}`)) {
		t.Errorf("value = %s", got.Value)
	}
	if len(idx.recorded) != 1 || idx.recorded[0].RedisKey != testKey().String() {
		t.Errorf("index not recorded: %+v", idx.recorded)
	}
	if idx.recorded[0].ExpiresAt != s.now().Add(7*24*time.Hour) {
		t.Errorf("expires_at = %v, want now+7d", idx.recorded[0].ExpiresAt)
	}
}

func TestPushRejectsOversize(t *testing.T) {
	s := newService(newFakeBackend(), &fakeIndex{})
	big := bytes.Repeat([]byte("a"), MaxSizeBytes+1)
	err := s.Push(context.Background(), testKey(), big, "application/json", nil)
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("256KB+1 push error = %v, want ErrTooLarge", err)
	}
}

func TestPushAcceptsExactlyMaxSize(t *testing.T) {
	s := newService(newFakeBackend(), &fakeIndex{})
	atMax := bytes.Repeat([]byte("a"), MaxSizeBytes)
	if err := s.Push(context.Background(), testKey(), atMax, "text/plain", nil); err != nil {
		t.Errorf("exactly MaxSizeBytes should be accepted: %v", err)
	}
}

func TestPushValidatesSchema(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []any{"rows"},
		"properties": map[string]any{
			"rows": map[string]any{"type": "integer"},
		},
	}
	s := newService(newFakeBackend(), &fakeIndex{})

	if err := s.Push(context.Background(), testKey(), []byte(`{"rows":100}`), "application/json", schema); err != nil {
		t.Errorf("conforming payload should pass: %v", err)
	}
	err := s.Push(context.Background(), testKey(), []byte(`{"rows":"oops"}`), "application/json", schema)
	if !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("schema-violating payload error = %v, want ErrSchemaMismatch", err)
	}
}
