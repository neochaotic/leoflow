package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/setup"
)

// newDoctorCommand reports the host platform, which dependencies are present,
// and the highest achievable operating tier — the diagnostic companion to
// `leoflow setup`.
func newDoctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Report host platform, dependencies, and the achievable operating tier.",
		Long: "doctor inspects the host (OS, architecture, libc), checks for Python 3.11, " +
			"Docker, k3d, and kubectl, and reports which operating tier is achievable. " +
			"It changes nothing; run `leoflow setup` to bootstrap.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r := setup.Detect(setup.Probe{
				GOOS:     runtime.GOOS,
				GOARCH:   runtime.GOARCH,
				LookPath: exec.LookPath,
				Stat:     os.Stat,
				Getwd:    os.Getwd,
			})
			renderDoctor(cmd.OutOrStdout(), r)
			return nil
		},
	}
}

// renderDoctor writes a human-readable diagnostic report. It is separated from
// the command so the formatting is unit-tested.
func renderDoctor(w io.Writer, r setup.Report) {
	p := func(format string, a ...any) {
		_, _ = fmt.Fprintf(w, format, a...) //nolint:errcheck // best-effort terminal report
	}

	plat := r.OS + "/" + r.Arch
	if r.Libc != "" {
		plat += " (" + r.Libc + ")"
	}
	p("leoflow doctor\n\n")
	p("  platform      %s\n", plat)
	p("  python 3.11   %s\n", found(r.Python311, r.PythonPath, "will download a relocatable CPython on `leoflow setup`"))
	p("  docker        %s\n", found(r.Docker, "found", "not found"))
	p("  k3d           %s\n", found(r.K3d, "found", "not found (fetched on demand for the k8s tier)"))
	p("  kubectl       %s\n", found(r.Kubectl, "found", "not found (fetched on demand for the k8s tier)"))

	p("\n  recommended tier: %s\n", r.Tier)
	p("    tier 0 subprocess  always available\n")
	p("    tier 1 docker      %s\n", availability(r.Docker))
	p("    tier 2 k8s         %s\n", availability(r.Docker))

	if r.UnderMnt {
		p("\n  WARNING: this directory is under /mnt (WSL). Keep your project in the WSL\n")
		p("  native filesystem (~/...) so `leoflow dev` hot-reload (inotify) works.\n")
	}
	p("\n  next: run `leoflow setup` to bootstrap the managed runtime.\n")
}

// found renders a present/absent line: when present it shows detail (a path or
// "found"), otherwise the absent hint.
func found(present bool, detail, absent string) string {
	if present {
		if detail == "" {
			return "found"
		}
		return "found" + ifPath(detail)
	}
	return absent
}

// ifPath wraps a filesystem path in parentheses; an empty or "found" detail
// renders nothing extra.
func ifPath(detail string) string {
	if detail == "" || detail == "found" {
		return ""
	}
	return " (" + detail + ")"
}

// availability renders whether a Docker-gated tier is reachable.
func availability(docker bool) string {
	if docker {
		return "available (Docker present; k3d/kubectl fetched on demand)"
	}
	return "needs Docker (not detected)"
}
