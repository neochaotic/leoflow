package cli

import "testing"

func TestInspectDigestArgs(t *testing.T) {
	args := inspectDigestArgs("ghcr.io/org/etl:v1")
	if args[0] != "inspect" {
		t.Errorf("args[0] = %q, want inspect", args[0])
	}
	if argIndex(args, "ghcr.io/org/etl:v1") < 0 {
		t.Errorf("image missing from args: %v", args)
	}
	// It must ask the builder for the repo digest, not the local image ID.
	if argIndex(args, "--format") < 0 {
		t.Errorf("want a --format flag selecting RepoDigests: %v", args)
	}
}

func TestParseDigestRefExtractsPinnedRef(t *testing.T) {
	got, err := parseDigestRef("ghcr.io/org/etl@sha256:deadbeefcafe\n")
	if err != nil {
		t.Fatalf("parseDigestRef: %v", err)
	}
	if got != "ghcr.io/org/etl@sha256:deadbeefcafe" {
		t.Errorf("ref = %q, want the trimmed repo@sha256 ref", got)
	}
}

func TestParseDigestRefRejectsEmpty(t *testing.T) {
	// An image with no RepoDigests (never pushed) yields an empty inspect result.
	if _, err := parseDigestRef("  \n"); err == nil {
		t.Error("expected an error when the image has no repo digest (not pushed)")
	}
}

func TestParseDigestRefRejectsUnpinned(t *testing.T) {
	if _, err := parseDigestRef("ghcr.io/org/etl:v1\n"); err == nil {
		t.Error("expected an error when the ref carries no @sha256 digest")
	}
}
