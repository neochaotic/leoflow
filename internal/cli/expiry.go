package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/neochaotic/leoflow/internal/version"
)

// releaseDownloadURL is where an expired alpha build points users for an upgrade.
const releaseDownloadURL = "https://github.com/neochaotic/leoflow/releases"

// expiryWarnWindow is how long before expiry the CLI starts warning on every
// command, so the expiry is never a surprise.
const expiryWarnWindow = 14 * 24 * time.Hour

// expiryIgnoreEnv lets operators override an expired build (CI, air-gapped
// re-runs) without rebuilding.
const expiryIgnoreEnv = "LEOFLOW_IGNORE_EXPIRY"

// expiryExemptCommands keep working past expiry so a user can still inspect the
// build and find the upgrade path.
var expiryExemptCommands = map[string]bool{
	"version":    true,
	"help":       true,
	"completion": true,
}

// expiryStatus resolves the build's expiry; it is a var so tests can inject a
// status without rebuilding the version package's link-time state.
var expiryStatus = version.ExpiryStatus

// enforceExpiry gates command execution on the build's baked-in expiry. Dev
// builds (no expiry) and exempt commands always pass; within the warning window
// it prints a notice and proceeds; past expiry it returns an error unless the
// command is exempt or LEOFLOW_IGNORE_EXPIRY is set.
func enforceExpiry(now time.Time, cmdName string, stderr io.Writer) error {
	set, at, expired := expiryStatus(now)
	if !set {
		return nil
	}
	day := at.Format("2006-01-02")
	if !expired {
		if at.Sub(now) <= expiryWarnWindow {
			_, _ = fmt.Fprintf(stderr, "warning: this alpha build expires on %s; grab a newer release at %s\n", day, releaseDownloadURL) //nolint:errcheck // best-effort terminal warning
		}
		return nil
	}
	if expiryExemptCommands[cmdName] || os.Getenv(expiryIgnoreEnv) != "" {
		_, _ = fmt.Fprintf(stderr, "warning: this alpha build expired on %s; running anyway\n", day) //nolint:errcheck // best-effort terminal warning
		return nil
	}
	return fmt.Errorf("this alpha build expired on %s; download a newer release at %s (or set %s=1 to override)", day, releaseDownloadURL, expiryIgnoreEnv)
}
