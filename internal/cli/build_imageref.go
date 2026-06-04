package cli

import (
	"fmt"
	"strings"
)

// composeImageRef builds the pushed image reference `<url>/<image_name>:<tag>`
// from the registry config, tolerating a trailing slash on the URL.
func composeImageRef(url, imageName, tag string) string {
	return fmt.Sprintf("%s/%s:%s", strings.TrimRight(url, "/"), imageName, tag)
}

// resolveImageTag picks the image tag from the registry's tag strategy. The
// default (and "git-sha"/"git_sha") prefers the immutable git sha, so each
// commit maps to a distinct artifact; it falls back to the DAG version when the
// project is not in git. "version" tags by the DAG version explicitly.
func resolveImageTag(strategy, version, gitSHA string) string {
	switch strategy {
	case "version":
		return version
	case "git_sha", "git-sha":
		return gitSHA
	default:
		if gitSHA != "" {
			return gitSHA
		}
		return version
	}
}
