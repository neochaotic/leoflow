package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/logs"
)

// fakeTailReader serves no stored logs and tails a fixed set of live lines.
type fakeTailReader struct{ ch chan string }

func (f *fakeTailReader) ReadLogs(context.Context, string, string, string, string, int) (io.ReadCloser, error) {
	return nil, ErrNotFound
}

func (f *fakeTailReader) Tail(context.Context, string, string, string, string, int) (lines <-chan string, cancel func(), err error) {
	return f.ch, func() {}, nil
}

// TestTailNdjsonEmitsStructuredEvents asserts live-tailed lines are emitted as
// NDJSON structured events carrying the level and stream-derived logger — so the
// live view colors lines like the stored drill-down.
func TestTailNdjsonEmitsStructuredEvents(t *testing.T) {
	ch := make(chan string, 2)
	ch <- logs.EncodeLine(logs.Event{Level: "error", Stream: "stdout", Message: "boom", Time: time.Now().UTC()})
	close(ch) // the loop returns once the channel drains

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/logs?follow=true", http.NoBody)

	tailNdjson(c, &fakeTailReader{ch: ch}, 1)

	body := rec.Body.String()
	if !strings.Contains(body, `"event":"boom"`) {
		t.Errorf("tailed line should carry the message, got %q", body)
	}
	if !strings.Contains(body, `"level":"error"`) {
		t.Errorf("tailed line should carry the level, got %q", body)
	}
	if !strings.Contains(body, `"logger":"task.stdout"`) {
		t.Errorf("tailed line should carry the stream-derived logger, got %q", body)
	}
}
