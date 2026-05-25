package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteParserConfig(t *testing.T) {
	t.Run("writes parser_cmd when config is absent", func(t *testing.T) {
		home := t.TempDir()
		wrote, err := writeParserConfig(home, "/v/bin/python -m leoflow_parser")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if !wrote {
			t.Fatal("wrote = false, want true on a fresh config")
		}
		data, rerr := os.ReadFile(filepath.Join(home, "config.yaml"))
		if rerr != nil {
			t.Fatalf("reading config: %v", rerr)
		}
		if !strings.Contains(string(data), "parser_cmd:") ||
			!strings.Contains(string(data), "leoflow_parser") {
			t.Errorf("config = %q, want parser_cmd entry", data)
		}
	})

	t.Run("leaves an existing config untouched", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, "config.yaml")
		original := "server_url: http://example\n"
		if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		wrote, err := writeParserConfig(home, "/v/bin/python -m leoflow_parser")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if wrote {
			t.Error("wrote = true, want false (must not clobber existing config)")
		}
		data, _ := os.ReadFile(path)
		if string(data) != original {
			t.Errorf("config changed to %q, want it preserved", data)
		}
	})
}
