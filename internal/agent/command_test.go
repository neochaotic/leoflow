package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildCommandPython(t *testing.T) {
	argv, err := BuildCommand("python", "tasks.extract:run")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"python", "-m", "leoflow_runtime", "tasks.extract:run"}
	if len(argv) != len(want) {
		t.Fatalf("unexpected argv: %v", argv)
	}
	for i, w := range want {
		if argv[i] != w {
			t.Errorf("argv[%d] = %q, want %q", i, argv[i], w)
		}
	}
}

func TestBuildCommandPythonHonorsInterpreterEnv(t *testing.T) {
	t.Setenv("LEOFLOW_PYTHON", "python3")
	argv, err := BuildCommand("python", "dag:extract")
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "python3" {
		t.Errorf("argv[0] = %q, want python3 (LEOFLOW_PYTHON)", argv[0])
	}
}

func TestBuildCommandPythonBadEntrypoint(t *testing.T) {
	for _, ep := range []string{"", "noseparator", ":run", "mod:"} {
		if _, err := BuildCommand("python", ep); err == nil {
			t.Errorf("entrypoint %q should be rejected", ep)
		}
	}
}

func TestBuildCommandBash(t *testing.T) {
	argv, err := BuildCommand("bash", "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 3 || argv[0] != "bash" || argv[1] != "-c" || argv[2] != "echo hi" {
		t.Errorf("unexpected argv: %v", argv)
	}
	if _, err := BuildCommand("bash", ""); err == nil {
		t.Error("empty bash command should be rejected")
	}
}

func TestBuildCommandHTTPAndUnknown(t *testing.T) {
	if _, err := BuildCommand("http_api", "x"); err == nil {
		t.Error("http_api should not be runnable by the agent")
	}
	if _, err := BuildCommand("ruby", "x"); err == nil {
		t.Error("unknown operator should be rejected")
	}
}

func TestXComEnvVar(t *testing.T) {
	if got := XComEnvVar("raw", []byte(`{"rows":3}`)); got != `LEOFLOW_XCOM_RAW={"rows":3}` {
		t.Errorf("XComEnvVar = %q", got)
	}
}

func TestReadReturnValue(t *testing.T) {
	if _, ok, err := ReadReturnValue(filepath.Join(t.TempDir(), "missing.json")); ok || err != nil {
		t.Errorf("missing file: ok=%v err=%v, want false,nil", ok, err)
	}
	p := filepath.Join(t.TempDir(), "rv.json")
	if err := os.WriteFile(p, []byte(`42`), 0o644); err != nil {
		t.Fatal(err)
	}
	v, ok, err := ReadReturnValue(p)
	if err != nil || !ok || string(v) != "42" {
		t.Errorf("present file: v=%q ok=%v err=%v", v, ok, err)
	}
}

func TestBackoff(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	for i, w := range want {
		got, ok := Backoff(i + 1)
		if !ok || got != w {
			t.Errorf("Backoff(%d) = %v,%v want %v,true", i+1, got, ok, w)
		}
	}
	if _, ok := Backoff(6); ok {
		t.Error("Backoff past max attempts should be false")
	}
	if _, ok := Backoff(0); ok {
		t.Error("Backoff(0) should be false")
	}
}
