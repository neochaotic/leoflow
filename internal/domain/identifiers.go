package domain

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidRunID reports a dag_run_id a caller may not use.
var ErrInvalidRunID = errors.New("invalid run_id")

// ValidateRunID checks a caller-supplied dag_run_id.
//
// The trigger endpoint accepts dag_run_id verbatim from the request body, and a
// run id ends up as a path segment in the log sink — so a value carrying a
// separator or a parent reference steers the control plane's own writes outside
// the log root, with content the caller also controls.
//
// The sink refuses such a value independently; this exists so the request fails
// as a readable 400 instead of creating a run that appears to work and then
// silently produces no logs. Two gates, different jobs: this one is UX, the
// sink's is the security boundary and must never be relaxed to match this.
//
// Separators are banned, punctuation is not: Airflow-generated ids embed an
// RFC3339 timestamp ("manual__2026-07-30T12:00:00+00:00"), so rejecting ':' or
// '+' would reject every run the scheduler creates.
func ValidateRunID(v string) error {
	switch {
	case v == "":
		return fmt.Errorf("%w: is empty", ErrInvalidRunID)
	case v == "." || v == "..":
		return fmt.Errorf("%w: is a parent or current directory reference", ErrInvalidRunID)
	case strings.ContainsAny(v, `/\`):
		return fmt.Errorf("%w: contains a path separator", ErrInvalidRunID)
	case strings.ContainsRune(v, 0):
		return fmt.Errorf("%w: contains a null byte", ErrInvalidRunID)
	case len(v) > 255:
		return fmt.Errorf("%w: exceeds 255 bytes", ErrInvalidRunID)
	}
	return nil
}
