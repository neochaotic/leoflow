package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/neochaotic/leoflow/internal/setup"
)

func TestDoctorHelpers(t *testing.T) {
	if got := found(true, "/usr/bin/python3.11", "absent"); got != "found (/usr/bin/python3.11)" {
		t.Errorf("found(present,path) = %q", got)
	}
	if got := found(true, "found", "absent"); got != "found" {
		t.Errorf("found(present,\"found\") = %q, want \"found\"", got)
	}
	if got := found(false, "x", "not found"); got != "not found" {
		t.Errorf("found(absent) = %q, want \"not found\"", got)
	}
	if got := availability(true); !strings.Contains(got, "available") {
		t.Errorf("availability(true) = %q", got)
	}
	if got := availability(false); !strings.Contains(got, "needs Docker") {
		t.Errorf("availability(false) = %q", got)
	}
}

func TestDoctorCommand(t *testing.T) {
	cmd := newDoctorCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor err = %v", err)
	}
	if !strings.Contains(out.String(), "leoflow doctor") || !strings.Contains(out.String(), "recommended tier") {
		t.Errorf("doctor output unexpected:\n%s", out.String())
	}
}

func TestRenderDoctor(t *testing.T) {
	t.Run("docker host recommends k8s and shows python path", func(t *testing.T) {
		var buf bytes.Buffer
		renderDoctor(&buf, setup.Report{
			OS: "linux", Arch: "amd64", Libc: "glibc",
			Python311: true, PythonPath: "/usr/bin/python3.11",
			Docker: true, Tier: setup.TierK8s,
		})
		out := buf.String()
		for _, want := range []string{
			"linux/amd64 (glibc)",
			"/usr/bin/python3.11",
			"recommended tier: k8s",
			"available (Docker present",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q\n---\n%s", want, out)
			}
		}
	})

	t.Run("no docker, musl, under /mnt shows fallbacks and WSL warning", func(t *testing.T) {
		var buf bytes.Buffer
		renderDoctor(&buf, setup.Report{
			OS: "linux", Arch: "arm64", Libc: "musl",
			Python311: false,
			Docker:    false, Tier: setup.TierSubprocess, UnderMnt: true,
		})
		out := buf.String()
		for _, want := range []string{
			"linux/arm64 (musl)",
			"will download a relocatable CPython",
			"recommended tier: subprocess",
			"needs Docker (not detected)",
			"WSL",
			"leoflow setup",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q\n---\n%s", want, out)
			}
		}
	})
}
