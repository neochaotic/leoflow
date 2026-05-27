package xcom

import "testing"

// TestGlobToLike pins the Redis-glob → SQL-LIKE translation the Postgres backend
// uses for List, including escaping of LIKE metacharacters that are literal in a
// glob so a key containing "%" or "_" is matched literally.
func TestGlobToLike(t *testing.T) {
	cases := map[string]string{
		`xcom:t:d:r:*`:   `xcom:t:d:r:%`,   // glob * -> LIKE %
		`xcom:t:d:r:k?x`: `xcom:t:d:r:k_x`, // glob ? -> LIKE _
		`a%b_c`:          `a\%b\_c`,        // literal % and _ are escaped
		`plain`:          `plain`,
		`*`:              `%`,
	}
	for in, want := range cases {
		if got := globToLike(in); got != want {
			t.Errorf("globToLike(%q) = %q, want %q", in, got, want)
		}
	}
}
