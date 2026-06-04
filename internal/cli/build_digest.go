package cli

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// inspectDigestArgs assembles the builder argv that prints a pushed image's
// repo digest (repo@sha256:...). RepoDigests is populated only after a push, so
// this must run post-push. Works across docker/podman/nerdctl, which all expose
// RepoDigests via `inspect --format`.
func inspectDigestArgs(image string) []string {
	return []string{"inspect", "--format", "{{index .RepoDigests 0}}", image}
}

// parseDigestRef validates and trims the inspect output into a pinned image
// reference. An empty result means the image was never pushed (no RepoDigests);
// a result without an @sha256 digest is not a pin. Both are loud errors so a
// deploy never registers an unpinned image.
func parseDigestRef(out string) (string, error) {
	ref := strings.TrimSpace(out)
	if ref == "" || ref == "<no value>" {
		return "", fmt.Errorf("image has no repo digest (was it pushed?)")
	}
	if !strings.Contains(ref, "@sha256:") {
		return "", fmt.Errorf("inspect returned %q, which is not a digest-pinned reference", ref)
	}
	return ref, nil
}

// imageDigest shells out to the builder to resolve image's pinned repo digest
// after a push (ADR 0015: no Docker SDK). The returned reference is what deploy
// writes into dag.json so Pro pulls exactly the bytes that were built.
func imageDigest(cmd *cobra.Command, builder, image string) (string, error) {
	//nolint:gosec // G204: builder is operator-configured by design (ADR 0015).
	ic := exec.CommandContext(cmdContext(cmd), builder, inspectDigestArgs(image)...)
	var out bytes.Buffer
	ic.Stdout = &out
	ic.Stderr = cmd.ErrOrStderr()
	if err := ic.Run(); err != nil {
		return "", fmt.Errorf("inspecting image %q with %q: %w", image, builder, err)
	}
	return parseDigestRef(out.String())
}
