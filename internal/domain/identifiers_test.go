package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateRunID(t *testing.T) {
	for _, tc := range []struct {
		name  string
		id    string
		valid bool
	}{
		{"airflow scheduled", "scheduled__2026-07-30T12:00:00+00:00", true},
		{"airflow manual", "manual__2026-07-30T12:00:00+00:00", true},
		{"plain", "run-1", true},
		{"dots inside", "run.1.retry", true},
		{"empty", "", false},
		{"current dir", ".", false},
		{"parent dir", "..", false},
		{"parent traversal", "../../etc", false},
		{"forward slash", "a/b", false},
		{"backslash", `a\b`, false},
		{"absolute", "/etc/cron.d/x", false},
		{"null byte", "a\x00b", false},
		{"too long", strings.Repeat("a", 256), false},
		{"at the length limit", strings.Repeat("a", 255), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRunID(tc.id)
			if tc.valid && err != nil {
				t.Fatalf("ValidateRunID(%q) = %v, want nil", tc.id, err)
			}
			if !tc.valid {
				if err == nil {
					t.Fatalf("ValidateRunID(%q) = nil, want an error", tc.id)
				}
				if !errors.Is(err, ErrInvalidRunID) {
					t.Errorf("error does not wrap ErrInvalidRunID: %v", err)
				}
			}
		})
	}
}
