package observability

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
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

// TestHandlersEscapeNewlinesInAttributeValues pins the property that makes the
// OIDC deny path safe to log an IdP-supplied claim.
//
// internal/api/oidc_handler.go's denyWithCause logs the email, user id and
// tenant from a token, which are attacker-influenced: anyone who can register
// with the IdP chooses them, and on the arms that reject before verification
// nobody has vouched for them at all. Code scanning flags that as log injection
// (go/log-injection, alert 546), and it is a false positive only because both
// handlers this package builds quote a value that needs it, so a claim carrying
// a newline cannot forge a second log line.
//
// That is a property of the HANDLER, not of the call site. A future switch to a
// handler that writes values raw would make the same call site injectable with
// no diff anywhere near it, and the alert is dismissed, so nothing would say so.
// This is what says so.
func TestHandlersEscapeNewlinesInAttributeValues(t *testing.T) {
	// The payload a forged line needs: close the quote, start a new record, and
	// claim the login succeeded as somebody else.
	const forged = "victim@example.com\"\ntime=2026-01-01T00:00:00Z level=INFO msg=\"oidc: login granted\" user=admin"

	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			slog.New(newHandler(&buf, "info", format)).Warn("oidc: login denied", "email", forged)

			out := buf.String()
			if got := strings.Count(strings.TrimRight(out, "\n"), "\n"); got != 0 {
				t.Fatalf("one Warn produced %d extra lines; the handler passed a newline through, so an IdP claim can forge log records:\n%s", got, out)
			}
			if strings.Contains(out, `msg="oidc: login granted"`) || strings.Contains(out, `"msg":"oidc: login granted"`) {
				t.Fatalf("the forged record appears as a record rather than as escaped text:\n%s", out)
			}
			// The value must still be READABLE: an operator reading this line is
			// the reason it is logged at all. Escaped, not dropped.
			if !strings.Contains(out, "victim@example.com") {
				t.Fatalf("the email is not in the output at all; escaping must not mean discarding:\n%s", out)
			}
		})
	}
}
