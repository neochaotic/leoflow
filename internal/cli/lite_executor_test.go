package cli

import "testing"

// TestResolveExecutor pins the auto-detect default: `leoflow lite` (executor
// "auto") uses k3d when Docker is available, else falls back to the subprocess
// executor so Lite still runs Docker-free; an explicit choice is honored.
func TestResolveExecutor(t *testing.T) {
	cases := []struct {
		flag   string
		docker bool
		want   string
	}{
		{"auto", true, "k8s"},
		{"auto", false, "subprocess"},
		{"k8s", false, "k8s"},
		{"subprocess", true, "subprocess"},
	}
	for _, c := range cases {
		if got := resolveExecutor(c.flag, c.docker); got != c.want {
			t.Errorf("resolveExecutor(%q, %v) = %q, want %q", c.flag, c.docker, got, c.want)
		}
	}
}
