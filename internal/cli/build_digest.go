package cli

import (
	"fmt"
	"strings"
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
