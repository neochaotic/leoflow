package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParsePythonMinor covers the `python_version` field as it is actually
// written: "3.13". A bare major, a patch-qualified value and junk are all
// rejected rather than coerced, because the value decides which interpreter a
// task runs on and a silent coercion is exactly the class this fix exists to
// remove.
func TestParsePythonMinor(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		ok   bool
	}{
		{"3.11", 11, true},
		{"3.13", 13, true},
		{"3.9", 9, true},
		{"3", 0, false},
		{"3.13.2", 0, false},
		{"", 0, false},
		{"py3.13", 0, false},
		{"3.x", 0, false},
	} {
		got, err := parsePythonMinor(tc.in)
		if tc.ok && err != nil {
			t.Errorf("parsePythonMinor(%q) error = %v, want %d", tc.in, err, tc.want)
			continue
		}
		if !tc.ok && err == nil {
			t.Errorf("parsePythonMinor(%q) = %d, want an error", tc.in, got)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("parsePythonMinor(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestResolvePythonForVersion is the regression for #1092: `leoflow dev` built
// every venv on the managed CPython 3.11 whatever the project declared, so a
// project pinned to 3.13 ran on 3.13 in the cluster and 3.11 locally.
//
// The precedence under a declared version differs from the no-preference one on
// exactly one point, and it is the point that mattered: the managed build is
// trusted by PATH when nothing was asked for, and must be version-CHECKED when
// something was. Trusting the path is what produced the skew.
func TestResolvePythonForVersion(t *testing.T) {
	ctx := context.Background()
	absent := func(string) (string, error) { return "", os.ErrNotExist }

	// managedAt writes a fake interpreter and reports the version it claims.
	managedAt := func(t *testing.T, claims int) (string, func(context.Context, string) (int, int, error)) {
		t.Helper()
		p := filepath.Join(t.TempDir(), "python3.11")
		if err := os.WriteFile(p, []byte("#!/fake"), 0o700); err != nil {
			t.Fatal(err)
		}
		return p, func(context.Context, string) (int, int, error) { return 3, claims, nil }
	}

	t.Run("the managed build is used when it IS the requested minor", func(t *testing.T) {
		managed, ver := managedAt(t, 11)
		got, err := resolvePythonFor(ctx, 11, managed, absent, ver)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != managed {
			t.Errorf("path = %q, want the managed build %q", got, managed)
		}
	})

	t.Run("the managed build is REJECTED when it is not the requested minor", func(t *testing.T) {
		// The bug: 3.11 on disk, 3.13 requested, 3.13 available on PATH. Before
		// the fix the managed path won on os.Stat alone and the venv was 3.11.
		managed, _ := managedAt(t, 11)
		lookPath := func(name string) (string, error) {
			if name == "python3.13" {
				return "/usr/bin/python3.13", nil
			}
			return "", os.ErrNotExist
		}
		ver := func(_ context.Context, p string) (int, int, error) {
			if p == managed {
				return 3, 11, nil
			}
			return 3, 13, nil
		}
		got, err := resolvePythonFor(ctx, 13, managed, lookPath, ver)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != "/usr/bin/python3.13" {
			t.Errorf("path = %q, want the host python3.13 — the managed 3.11 must not win a 3.13 request", got)
		}
	})

	t.Run("a host interpreter reporting the wrong minor is not accepted", func(t *testing.T) {
		// `python3` exists but is 3.11 while 3.13 was asked for. Accepting it is
		// the silent substitution the field exists to prevent.
		lookPath := func(name string) (string, error) {
			if name == "python3" {
				return "/usr/bin/python3", nil
			}
			return "", os.ErrNotExist
		}
		got, err := resolvePythonFor(ctx, 13, "", lookPath,
			func(context.Context, string) (int, int, error) { return 3, 11, nil })
		if err == nil {
			t.Fatalf("path = %q, err = nil; want a refusal naming the requested version", got)
		}
		if got != "" {
			t.Errorf("path = %q, want empty on refusal", got)
		}
		if !containsAll(err.Error(), "3.13", "3.11") {
			t.Errorf("error %q should name BOTH the version asked for and the one found", err)
		}
	})

	t.Run("nothing on the host names the version in the error", func(t *testing.T) {
		_, err := resolvePythonFor(ctx, 13, "", absent,
			func(context.Context, string) (int, int, error) { return 0, 0, os.ErrNotExist })
		if err == nil {
			t.Fatal("err = nil, want a refusal")
		}
		if !containsAll(err.Error(), "3.13") {
			t.Errorf("error %q must name the requested version so the user knows what to install", err)
		}
	})

	t.Run("bare python3 is accepted when it reports the requested minor", func(t *testing.T) {
		// Distros where only `python3` exists and it happens to be the right
		// minor must still work — the version, not the binary name, decides.
		lookPath := func(name string) (string, error) {
			if name == "python3" {
				return "/usr/bin/python3", nil
			}
			return "", os.ErrNotExist
		}
		got, err := resolvePythonFor(ctx, 13, "", lookPath,
			func(context.Context, string) (int, int, error) { return 3, 13, nil })
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != "/usr/bin/python3" {
			t.Errorf("path = %q, want /usr/bin/python3", got)
		}
	})
}

// TestPyvenvMinor reads the minor a venv was actually built with. Without it a
// venv created before the project changed `python_version` keeps serving the old
// interpreter forever: ensureDagVenv short-circuits on the interpreter file
// existing, so the declared version would apply only to venvs that never existed.
func TestPyvenvMinor(t *testing.T) {
	t.Run("reads the version line", func(t *testing.T) {
		dir := t.TempDir()
		cfg := "home = /opt/python/bin\ninclude-system-site-packages = false\nversion = 3.12.0\n"
		if err := os.WriteFile(filepath.Join(dir, "pyvenv.cfg"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		got, ok := pyvenvMinor(dir)
		if !ok || got != 12 {
			t.Errorf("pyvenvMinor = (%d, %v), want (12, true)", got, ok)
		}
	})

	t.Run("a venv with no pyvenv.cfg reports unknown, not zero", func(t *testing.T) {
		// Unknown must not read as "minor 0", which would rebuild every venv on
		// every boot.
		if got, ok := pyvenvMinor(t.TempDir()); ok {
			t.Errorf("pyvenvMinor = (%d, true), want ok=false for a missing pyvenv.cfg", got)
		}
	})

	t.Run("version_info is accepted when version is absent", func(t *testing.T) {
		// Some CPython builds write `version_info = 3.13.1.final.0` instead.
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "pyvenv.cfg"),
			[]byte("version_info = 3.13.1.final.0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, ok := pyvenvMinor(dir)
		if !ok || got != 13 {
			t.Errorf("pyvenvMinor = (%d, %v), want (13, true)", got, ok)
		}
	})
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// TestDagVenvRebuiltWhenDeclaredMinorChanges is the second half of #1092. The
// resolver alone fixes only venvs that do not exist yet: ensureDagVenv returns
// early when the interpreter file is present, so an author who edits
// `python_version` on a project they have already run would keep the old
// interpreter with no indication.
//
// The assertion is indirect on purpose. Asking for a minor no host has makes the
// rebuild observable without depending on which Pythons the test machine
// carries: the error can only be reached by (a) noticing the venv's minor
// disagrees, and (b) going back out to resolve a base for the declared one. A
// build that skipped the check would return the existing venv and no error.
func TestDagVenvRebuiltWhenDeclaredMinorChanges(t *testing.T) {
	home := t.TempDir()
	const dagID = "skewed"
	dir := dagVenvDir(home, dagID)
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// A venv that exists and works — built on 3.11.
	if err := os.WriteFile(filepath.Join(bin, "python"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pyvenv.cfg"), []byte("version = 3.11.15\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ensureDagVenv(context.Background(), devTestCmd(), home, dagID, "", "3.99", nil)
	if err == nil {
		t.Fatal("err = nil; the venv is on 3.11 while the project declares 3.99, so it must be rebuilt (and rebuilding must fail here, since no host has 3.99)")
	}
	if !containsAll(err.Error(), "3.99") {
		t.Errorf("error %q should name the declared version", err)
	}
	if _, serr := os.Stat(filepath.Join(bin, "python")); serr == nil {
		t.Error("the stale 3.11 venv is still on disk; a failed rebuild must not leave the wrong interpreter in place")
	}
}
