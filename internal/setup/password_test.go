package setup

import (
	"regexp"
	"testing"
)

func TestGenerateHumanPassword(t *testing.T) {
	// Easy to jot down: a short word + a few digits, lowercase letters and digits
	// only (no symbols, no ambiguous casing), at most 10 characters.
	re := regexp.MustCompile(`^[a-z]{3,6}\d{2,4}$`)
	seen := map[string]int{}
	for i := 0; i < 200; i++ {
		pw, err := GenerateHumanPassword()
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(pw) > 10 {
			t.Errorf("password %q is longer than 10 chars", pw)
		}
		if !re.MatchString(pw) {
			t.Errorf("password %q is not a word+digits of [a-z0-9]", pw)
		}
		seen[pw]++
	}
	// Not deterministic: 200 draws should not collapse to a single value.
	if len(seen) < 20 {
		t.Errorf("passwords look non-random: only %d distinct in 200 draws", len(seen))
	}
}
