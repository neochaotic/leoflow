package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/neochaotic/leoflow/internal/setup"
)

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
