package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestProvisionParserVenvReuse(t *testing.T) {
	home := t.TempDir()
	venvPy := filepath.Join(home, "parser-venv", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(venvPy), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(venvPy, []byte("#!/x"), 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	got, err := provisionParserVenv(&cobra.Command{}, &out, home, filepath.Join(home, "pysrc"), "/sys/python3.11")
	if err != nil {
		t.Fatalf("err = %v, want nil (reuse branch)", err)
	}
	if got != venvPy {
		t.Errorf("got %q, want existing venv python %q", got, venvPy)
	}
	if !strings.Contains(out.String(), "reusing") {
		t.Errorf("output = %q, want a reuse notice", out.String())
	}
}

func TestLibcSuffix(t *testing.T) {
	cases := map[string]string{"": "", "glibc": " (glibc)", "musl": " (musl)"}
	for in, want := range cases {
		if got := libcSuffix(in); got != want {
			t.Errorf("libcSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteSetupManifest(t *testing.T) {
	home := t.TempDir()
	want := setupManifest{
		Python: "/p/python3.11", Workspace: "/w", Tier: "k8s",
		OS: "linux", Arch: "amd64", ParserVenv: "/v", ParserCmd: "/v/bin/python -m leoflow_parser",
		UpdatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := writeSetupManifest(home, want); err != nil {
		t.Fatalf("writeSetupManifest err = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, "setup.json"))
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	var got setupManifest
	if uerr := json.Unmarshal(data, &got); uerr != nil {
		t.Fatalf("unmarshal: %v", uerr)
	}
	if got.Python != want.Python || got.Tier != want.Tier || got.ParserCmd != want.ParserCmd {
		t.Errorf("manifest round-trip mismatch: got %+v", got)
	}
}

func TestRunSetupSkipPythonDeps(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A fake python3.11 on PATH so EnsurePython uses it instead of downloading.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "python3.11"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cmd := newSetupCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	ws := filepath.Join(home, "ws")
	cmd.SetArgs([]string{"--skip-python-deps", "--workspace", ws})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup --skip-python-deps err = %v\n%s", err, out.String())
	}
	// Sources extracted, workspace + manifest created, parser venv skipped.
	for _, p := range []string{
		filepath.Join(home, ".leoflow", "pysrc", "parser", "pyproject.toml"),
		filepath.Join(home, ".leoflow", "setup.json"),
		ws,
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}
	if !strings.Contains(out.String(), "skipped") {
		t.Errorf("output should note the parser venv was skipped:\n%s", out.String())
	}
}

func TestRunSetupDryRun(t *testing.T) {
	cmd := newSetupCommand()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{"--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup --dry-run err = %v", err)
	}
	s := out.String()
	for _, want := range []string{"leoflow setup", "platform", "tier", "dry run"} {
		if !strings.Contains(s, want) {
			t.Errorf("dry-run output missing %q\n---\n%s", want, s)
		}
	}
}

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
