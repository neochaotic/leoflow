package domain

import "time"

// User is a control-plane account as returned by the admin create-user API. It
// never carries the password or its hash — those are write-only. Role is the
// single role granted at creation ("" when none was assigned); the RBAC model
// supports multiple roles per user, but create-user grants at most one.
type User struct {
	ID        string
	Email     string
	Role      string
	IsActive  bool
	CreatedAt time.Time
}
