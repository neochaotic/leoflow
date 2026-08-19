package domain

import "time"

// User is a control-plane account as returned by the admin user-management API.
// It never carries the password or its hash — those are write-only. Roles is the
// full set of role names the user holds: the list path aggregates every role
// grant, and the create path echoes back the roles it granted (empty when none
// were requested).
type User struct {
	ID        string
	Email     string
	Roles     []string
	IsActive  bool
	CreatedAt time.Time
}
