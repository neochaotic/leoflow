package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	leoflow "github.com/neochaotic/leoflow"
	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/setup"
)

// liteSettings are the Lite-edition choices the setup wizard gathers.
type liteSettings struct {
	Workspace  string
	Executor   string // "subprocess" (local) or "k8s" (mini-cluster)
	AdminEmail string
	Port       int
}

// setupManifest records what `leoflow setup` provisioned, so later runs and
// `leoflow doctor` can report the managed state.
type setupManifest struct {
	Python     string    `json:"python"`
	Workspace  string    `json:"workspace"`
	Tier       string    `json:"tier"`
	OS         string    `json:"os"`
	Arch       string    `json:"arch"`
	Executor   string    `json:"executor,omitempty"`
	Port       int       `json:"port,omitempty"`
	AdminEmail string    `json:"admin_email,omitempty"`
	ParserCmd  string    `json:"parser_cmd,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// promptLine asks for a value with a default; an empty answer keeps the default.
func promptLine(in *bufio.Reader, out io.Writer, label, def string) string {
	_, _ = fmt.Fprintf(out, "  %s [%s]: ", label, def) //nolint:errcheck // best-effort prompt
	line, _ := in.ReadString('\n')                     //nolint:errcheck // empty -> default
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	return def
}

// gatherLiteConfig resolves the Lite settings: it returns the defaults verbatim
// when non-interactive (e.g. `curl | sh`), and otherwise prompts on the TTY,
// guiding the executor choice. It is pure (injected reader/writer) so it is
// unit-tested for both paths.
func gatherLiteConfig(interactive bool, in *bufio.Reader, out io.Writer, def liteSettings) liteSettings {
	if !interactive {
		return def
	}
	_, _ = fmt.Fprintln(out, "\nLeoflow Lite setup — press Enter to accept each [default].") //nolint:errcheck // best-effort
	s := def
	s.Workspace = promptLine(in, out, "Workspace directory (your DAG projects)", def.Workspace)
	_, _ = fmt.Fprintln(out, "  Executor: 'subprocess' = local use (fast, no Docker); 'k8s' = mini-cluster for development (k3d, needs Docker).") //nolint:errcheck // best-effort
	for {
		s.Executor = promptLine(in, out, "Executor (subprocess|k8s)", def.Executor)
		if s.Executor == "subprocess" || s.Executor == "k8s" {
			break
		}
		_, _ = fmt.Fprintln(out, "  please type 'subprocess' or 'k8s'") //nolint:errcheck // best-effort
	}
	if p, err := strconv.Atoi(promptLine(in, out, "UI port", strconv.Itoa(def.Port))); err == nil && p > 0 {
		s.Port = p
	}
	s.AdminEmail = promptLine(in, out, "Admin email", def.AdminEmail)
	return s
}

// isInteractive reports whether f is a real terminal (so prompting makes sense).
// Using x/term distinguishes a TTY from a pipe (`curl | sh`) or /dev/null, which
// a ModeCharDevice check would wrongly treat as interactive.
func isInteractive(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// newSetupCommand bootstraps the managed runtime: a usable Python 3.11 (the
// system one or a downloaded relocatable CPython), the ~/.leoflow layout, and a
// workspace directory for the user's DAG projects.
func newSetupCommand() *cobra.Command {
	var workspace string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Bootstrap the managed Leoflow runtime (Python, parser, workspace).",
		Long: "setup prepares ~/.leoflow: it ensures a Python 3.11 is available " +
			"(using a system interpreter if present, otherwise downloading a pinned, " +
			"checksum-verified relocatable CPython — no sudo, no system packages), " +
			"extracts the embedded parser and runtime sources, points the parser at the " +
			"interpreter (pure Python, no venv, no Airflow — ADR 0024), and creates a " +
			"workspace directory. Re-running is safe; --dry-run shows the plan.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetup(cmd, workspace, dryRun)
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "workspace dir for your DAG projects (default ~/leoflow)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "detect and print the plan without downloading or writing anything")
	return cmd
}

func runSetup(cmd *cobra.Command, workspaceFlag string, dryRun bool) error {
	out := cmd.OutOrStdout()
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home directory: %w", err)
	}
	leoflowHome := filepath.Join(homeDir, ".leoflow")

	r := setup.Detect(setup.Probe{
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		LookPath: exec.LookPath, Stat: os.Stat, Getwd: os.Getwd,
	})

	def := liteSettings{Workspace: workspaceFlag, Executor: "subprocess", AdminEmail: "admin@leoflow.local", Port: 8088}
	if def.Workspace == "" {
		def.Workspace = filepath.Join(homeDir, "leoflow")
	}

	_, _ = fmt.Fprintf(out, "leoflow setup\n\n  platform   %s/%s%s\n", r.OS, r.Arch, libcSuffix(r.Libc)) //nolint:errcheck // best-effort terminal output

	// Prompt only on first setup. On a re-run the config already exists and is not
	// rewritten, so re-asking would silently discard the answers — instead keep the
	// recorded settings (and point at reset-password to change the admin).
	firstRun := !liteConfigExists(leoflowHome)
	var lc liteSettings
	if firstRun {
		interactive := isInteractive(os.Stdin) && !dryRun
		lc = gatherLiteConfig(interactive, bufio.NewReader(os.Stdin), out, def)
	} else {
		lc = loadManifestSettings(leoflowHome, def)
		_, _ = fmt.Fprintln(out, "\n  already configured (~/.leoflow/config.yaml) — keeping your settings.\n  change the admin with `sudo leoflow lite reset-password`.") //nolint:errcheck // best-effort terminal output
	}

	_, _ = fmt.Fprintf(out, "\n  workspace  %s\n  executor   %s\n  port       %d\n  admin      %s\n", lc.Workspace, lc.Executor, lc.Port, lc.AdminEmail) //nolint:errcheck // best-effort terminal output
	if r.Python311 {
		_, _ = fmt.Fprintf(out, "  python     using system %s\n", r.PythonPath) //nolint:errcheck // best-effort terminal output
	} else {
		_, _ = fmt.Fprintln(out, "  python     none on PATH; will install a relocatable CPython 3.11 under ~/.leoflow/python") //nolint:errcheck // best-effort terminal output
	}

	if dryRun {
		_, _ = fmt.Fprintln(out, "\n  (dry run: nothing downloaded or written)") //nolint:errcheck // best-effort terminal output
		return nil
	}

	generated, perr := provisionLite(cmd, out, leoflowHome, r, lc)
	if perr != nil {
		return perr
	}

	if r.UnderMnt {
		_, _ = fmt.Fprintln(out, "\n  WARNING: under /mnt (WSL): keep DAG projects in the WSL native FS (~/...) for hot-reload.") //nolint:errcheck // best-effort terminal output
	}
	printSetupSummary(out, lc, generated)
	return nil
}

// provisionLite does the on-disk work: ensure Python, extract the parser/runtime
// sources, and (on first setup) generate the admin password, store only its hash,
// and write the config. It returns the generated plaintext password (empty on a
// re-run, where the existing config is left intact).
func provisionLite(cmd *cobra.Command, out io.Writer, leoflowHome string, r setup.Report, lc liteSettings) (string, error) {
	if mkErr := os.MkdirAll(leoflowHome, 0o750); mkErr != nil {
		return "", fmt.Errorf("creating %s: %w", leoflowHome, mkErr)
	}
	pyPath, pyErr := setup.EnsurePython(cmd.Context(), setup.EnsureOpts{
		Home: leoflowHome, GOOS: r.OS, GOARCH: r.Arch, Libc: r.Libc,
		LookPath: exec.LookPath, Stat: os.Stat,
		Logf: func(format string, a ...any) {
			_, _ = fmt.Fprintf(out, "  "+format+"\n", a...) //nolint:errcheck // best-effort terminal output
		},
	})
	if pyErr != nil {
		return "", fmt.Errorf("ensuring Python: %w", pyErr)
	}

	pysrcDir := filepath.Join(leoflowHome, "pysrc")
	if exErr := setup.ExtractFS(leoflow.PythonSources(), pysrcDir); exErr != nil {
		return "", fmt.Errorf("extracting embedded Python sources: %w", exErr)
	}
	_, _ = fmt.Fprintf(out, "  sources    extracted parser + runtime to %s\n", pysrcDir) //nolint:errcheck // best-effort terminal output

	// Fetch the Monaco editor bundle for the Lite web editor (ADR 0025).
	// Best-effort: an offline install still succeeds; the editor page shows a
	// `leoflow setup` hint until the bundle is present.
	if _, mErr := setup.EnsureMonaco(cmd.Context(), nil, leoflowHome, func(format string, a ...any) {
		_, _ = fmt.Fprintf(out, "  "+format+"\n", a...) //nolint:errcheck // best-effort terminal output
	}); mErr != nil {
		_, _ = fmt.Fprintf(out, "  WARNING: editor assets not fetched (%v) — the web editor will be unavailable until you re-run setup with network.\n", mErr) //nolint:errcheck // best-effort terminal output
	}

	// The parser is pure Python with vendored deps (ADR 0024) — no venv, no Airflow.
	parserCmd := fmt.Sprintf("env PYTHONPATH=%s %s -m leoflow_parser", filepath.Join(pysrcDir, "parser"), pyPath)

	// On first setup, generate the admin password, store ONLY its hash, and return
	// the plaintext for the one-time display. A re-run leaves the config untouched.
	var generated string
	if !liteConfigExists(leoflowHome) {
		pw, hash, herr := generateAdminCredential()
		if herr != nil {
			return "", herr
		}
		if wErr := writeLiteConfig(leoflowHome, parserCmd, lc, hash); wErr != nil {
			return "", fmt.Errorf("writing config: %w", wErr)
		}
		generated = pw
	}

	if wsErr := os.MkdirAll(lc.Workspace, 0o750); wsErr != nil {
		return "", fmt.Errorf("creating workspace %s: %w", lc.Workspace, wsErr)
	}
	if wErr := writeSetupManifest(leoflowHome, setupManifest{
		Python: pyPath, Workspace: lc.Workspace, Tier: r.Tier.String(),
		OS: r.OS, Arch: r.Arch, Executor: lc.Executor, Port: lc.Port,
		AdminEmail: lc.AdminEmail, ParserCmd: parserCmd, UpdatedAt: time.Now().UTC(),
	}); wErr != nil {
		return "", fmt.Errorf("writing setup manifest: %w", wErr)
	}
	return generated, nil
}

// generateAdminCredential returns a humanized plaintext password and its bcrypt
// hash. The plaintext is shown once; only the hash is persisted.
func generateAdminCredential() (plaintext, hash string, err error) {
	plaintext, err = setup.GenerateHumanPassword()
	if err != nil {
		return "", "", fmt.Errorf("generating admin password: %w", err)
	}
	hash, err = auth.HashPassword(plaintext)
	if err != nil {
		return "", "", fmt.Errorf("hashing admin password: %w", err)
	}
	return plaintext, hash, nil
}

// printSetupSummary closes setup with the admin credentials (shown once) and the
// network-exposure warning, or — on a re-run — points at reset-password.
func printSetupSummary(out io.Writer, lc liteSettings, generatedPassword string) {
	if generatedPassword != "" {
		_, _ = fmt.Fprintf(out, "\n  ── Leoflow Lite admin (save this — it is shown only once) ──\n    user:     %s\n    password: %s\n", lc.AdminEmail, generatedPassword) //nolint:errcheck // best-effort terminal output
		_, _ = fmt.Fprintln(out, "    (forgot it? `sudo leoflow lite reset-password`)")                                                                                     //nolint:errcheck // best-effort terminal output
	} else {
		_, _ = fmt.Fprintln(out, "\n  admin already configured (~/.leoflow/config.yaml); reset with `sudo leoflow lite reset-password`.") //nolint:errcheck // best-effort terminal output
	}
	_, _ = fmt.Fprintln(out, "\n  SECURITY: Lite uses a short, human-friendly password and is meant for local/")     //nolint:errcheck // best-effort terminal output
	_, _ = fmt.Fprintln(out, "  trusted use only. Run it on an internal network or VPN — never expose it publicly.") //nolint:errcheck // best-effort terminal output
	_, _ = fmt.Fprintf(out, "\n  ready. Next: `leoflow lite %s/<your-dag>`\n", lc.Workspace)                         //nolint:errcheck // best-effort terminal output
}

// liteConfigExists reports whether ~/.leoflow/config.yaml is already present.
func liteConfigExists(leoflowHome string) bool {
	_, err := os.Stat(filepath.Join(leoflowHome, "config.yaml"))
	return err == nil
}

// loadManifestSettings reads the previously-recorded Lite settings from
// setup.json (used on a re-run so prompts are not repeated), falling back to def.
func loadManifestSettings(leoflowHome string, def liteSettings) liteSettings {
	data, err := os.ReadFile(filepath.Join(leoflowHome, "setup.json"))
	if err != nil {
		return def
	}
	var m setupManifest
	if json.Unmarshal(data, &m) != nil {
		return def
	}
	out := def
	if m.Workspace != "" {
		out.Workspace = m.Workspace
	}
	if m.Executor != "" {
		out.Executor = m.Executor
	}
	if m.Port != 0 {
		out.Port = m.Port
	}
	if m.AdminEmail != "" {
		out.AdminEmail = m.AdminEmail
	}
	return out
}

// writeLiteConfig writes the Lite settings to ~/.leoflow/config.yaml (0600). Only
// the bcrypt hash of the admin password is stored — never the plaintext.
func writeLiteConfig(leoflowHome, parserCmd string, lc liteSettings, adminHash string) error {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "# Written by `leoflow setup` (Leoflow Lite).\n")
	_, _ = fmt.Fprintf(&b, "parser_cmd: %q\n", parserCmd)
	_, _ = fmt.Fprintf(&b, "workspace: %q\n", lc.Workspace)
	_, _ = fmt.Fprintf(&b, "lite_executor: %q\n", lc.Executor)
	_, _ = fmt.Fprintf(&b, "lite_port: %d\n", lc.Port)
	_, _ = fmt.Fprintf(&b, "admin_email: %q\n", lc.AdminEmail)
	_, _ = fmt.Fprintf(&b, "admin_password_hash: %q\n", adminHash)
	return os.WriteFile(filepath.Join(leoflowHome, "config.yaml"), []byte(b.String()), 0o600)
}

// writeSetupManifest persists the provisioning manifest to ~/.leoflow/setup.json.
func writeSetupManifest(leoflowHome string, m setupManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling manifest: %w", err)
	}
	return os.WriteFile(filepath.Join(leoflowHome, "setup.json"), data, 0o600)
}

// libcSuffix renders " (musl)" / " (glibc)" or nothing for platforms without a
// reported libc (darwin).
func libcSuffix(libc string) string {
	if libc == "" {
		return ""
	}
	return " (" + libc + ")"
}
