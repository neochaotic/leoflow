package domain

import "time"

// User is a control-plane account as returned by the admin user-management API.
// It never carries the password or its hash — those are write-only. Role is the
// single role granted at creation ("" when none was assigned); the RBAC model
// supports multiple roles per user, but create-user grants at most one. Roles is
// the full set of roles a user holds, populated by the list path (which
// aggregates every role grant); the create path leaves it nil and reports the
// one role it granted through Role.
type User struct {
	ID        string
	Email     string
	Role      string
	Roles     []string
	IsActive  bool
	CreatedAt time.Time
}
