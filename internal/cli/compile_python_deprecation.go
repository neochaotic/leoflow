package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/neochaotic/leoflow/internal/domain"
)

// deprecationWrapCols is where the reason paragraph is folded. Narrow enough to
// stay readable in a split terminal, wide enough that the sentence structure of
// the reason survives.
const deprecationWrapCols = 78

// warnDeprecatedPython prints a one-time warning when the base image this
// compile will build FROM is a Python line that is on its way out.
//
// It runs at compile, not at task boot. The choice matters: the author picking
// `python_version` is the only person who can change it, and compile is the
// moment they make the choice, with the file open. A warning from the agent in
// the task pod would reach the operator instead — who cannot edit the DAG's
// leoflow.yaml — and would repeat on every task of every run of every DAG,
// which is how a real warning becomes log noise people filter out. A build-time
// warning inside runtime/Dockerfile was the third option and is weaker still:
// it only fires for people who build the base from a source checkout, which is
// nobody on the path this deprecation is about (they pull the published tag).
//
// Nothing here fails the compile. The line is still published, still pulls, and
// still works; the point is to reach the author months before it stops being
// rebuilt, not to break them today.
//
// Two populations get the warning:
//   - the default path, where base_image is unset and the FROM is derived from
//     python_version;
//   - an explicit base_image that pins one of OUR published deprecated tags,
//     which is the population the deprecation is actually about — a pin is what
//     keeps a user on a frozen base after we stop publishing it.
//
// An explicit base_image pointing anywhere else is somebody else's image and
// gets no warning, even when python_version is set: the field is unused for the
// FROM in that case, and nagging about it would train people to ignore this.
func warnDeprecatedPython(w io.Writer, cfg *domain.LeoflowConfig) error {
	version, ok := deprecatedPythonForBuild(cfg)
	if !ok {
		return nil
	}
	d, ok := domain.DeprecatedPythonVersion(version)
	if !ok {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "warning: python_version %s is deprecated; %s:py%s stops being published after %s.\n",
		d.Version, publishedBaseRepo, d.Version, d.RemoveAfter)
	for _, line := range wrapWords(d.Reason, deprecationWrapCols) {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	fmt.Fprintf(&b, "  Fix: set python_version: \"%s\" in leoflow.yaml and rebuild. "+
		"Existing images keep running.\n", d.Replacement)
	_, err := io.WriteString(w, b.String())
	return err
}

// deprecatedPythonForBuild returns the Python line whose deprecation applies to
// this config's build, and whether one does at all. See warnDeprecatedPython for
// why an unrelated base_image opts out.
func deprecatedPythonForBuild(cfg *domain.LeoflowConfig) (string, bool) {
	if cfg == nil {
		return "", false
	}
	if cfg.BaseImage == "" {
		return cfg.PythonVersion, cfg.PythonVersion != ""
	}
	repo, tag, found := strings.Cut(cfg.BaseImage, ":")
	if !found || repo != publishedBaseRepo || !strings.HasPrefix(tag, "py") {
		return "", false
	}
	// Both published tag shapes name the line first: the moving `py3.10` and the
	// immutable per-release `py3.10-v0.4.5`.
	line, _, _ := strings.Cut(strings.TrimPrefix(tag, "py"), "-")
	return line, line != ""
}

// wrapWords folds s onto lines of at most cols runes, breaking only at spaces. A
// single word longer than cols is left over-long rather than cut, because the
// long tokens in these messages are image references and URLs, and a broken one
// cannot be copied.
func wrapWords(s string, cols int) []string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil
	}
	lines := []string{fields[0]}
	for _, word := range fields[1:] {
		last := len(lines) - 1
		if len([]rune(lines[last]))+1+len([]rune(word)) <= cols {
			lines[last] += " " + word
			continue
		}
		lines = append(lines, word)
	}
	return lines
}
