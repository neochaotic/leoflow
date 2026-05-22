package observability

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestNewHandlerLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newHandler(&buf, "warn", "json"))
	logger.Info("dropped")
	logger.Warn("kept")
	if bytes.Contains(buf.Bytes(), []byte("dropped")) {
		t.Error("info line should be filtered at warn level")
	}
	if !bytes.Contains(buf.Bytes(), []byte("kept")) {
		t.Error("warn line should be emitted at warn level")
	}
}

func TestNewHandlerJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newHandler(&buf, "info", "json"))
	logger.Info("hello", "dag_id", "etl")
	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not JSON: %v (%q)", err, buf.String())
	}
	if entry["msg"] != "hello" || entry["dag_id"] != "etl" {
		t.Errorf("unexpected entry: %v", entry)
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
		"":      slog.LevelInfo,
		"WARN":  slog.LevelWarn,
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}
