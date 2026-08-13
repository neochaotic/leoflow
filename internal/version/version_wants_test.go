package version

import "testing"

func TestWantsVersion(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"long flag", []string{"--version"}, true},
		{"single-dash flag", []string{"-version"}, true},
		{"bare subcommand", []string{"version"}, true},
		{"amid other args", []string{"--transport", "stdio", "--version"}, true},
		{"none", []string{"--transport", "stdio"}, false},
		{"empty", nil, false},
		{"not a version-looking flag", []string{"--verbose"}, false},
		{"substring is not a match", []string{"versioning"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WantsVersion(tc.args); got != tc.want {
				t.Errorf("WantsVersion(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestWantsHelp(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"long flag", []string{"--help"}, true},
		{"short flag", []string{"-h"}, true},
		{"single-dash long", []string{"-help"}, true},
		{"amid other args", []string{"--transport", "stdio", "--help"}, true},
		{"none", []string{"--transport", "stdio"}, false},
		{"empty", nil, false},
		{"not a help flag", []string{"--host"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WantsHelp(tc.args); got != tc.want {
				t.Errorf("WantsHelp(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
