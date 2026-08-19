package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/domain"
)

// minPasswordLength is the minimum length the create-user API accepts. It is a
// deliberately conservative floor, not a full password policy — the maintainer
// may tighten it later without breaking the endpoint's contract.
const minPasswordLength = 8

// emailPattern is a deliberately basic sanity check — one "@", a dotted domain,
// and no whitespace. It rejects obvious garbage; it is not a full RFC 5322
// validator (real deliverability is out of scope for account creation).
var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// normalizeEmail lowercases and trims the address so uniqueness (which is
// exact-text at the database) treats "Alice@X.com" and "alice@x.com" as one
// account rather than two.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// UserStore creates control-plane accounts for the admin create-user API. The
// store hashes the plaintext password (reusing the bootstrap admin's bcrypt
// scheme) and returns the created user without any secret. A duplicate email
// must surface as domain.ErrConflict and an unknown role as domain.ErrValidation.
type UserStore interface {
	CreateUser(ctx context.Context, tenant, email, password, role string) (domain.User, error)
	ListUsers(ctx context.Context, tenant string, limit, offset int) ([]domain.User, int, error)
}

// UserAuditWriter records account-creation events for the Audit Log. It is a
// separate, narrow interface (not the task-shaped AuditWriter) so account
// management writes a "user" resource entry with the acting admin as owner.
type UserAuditWriter interface {
	RecordUserCreatedAudit(ctx context.Context, tenant, actorUserID, createdUserID, email, role string) error
}

// createUserBody is the POST /api/v2/users payload. Password is write-only.
type createUserBody struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// userDTO is the create-user response. It never includes the password or hash.
type userDTO struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	IsActive  bool   `json:"is_active"`
	CreatedAt string `json:"created_at"`
}

func toUserDTO(u domain.User) userDTO {
	return userDTO{
		ID:        u.ID,
		Email:     u.Email,
		Role:      u.Role,
		IsActive:  u.IsActive,
		CreatedAt: u.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func createUserHandler(store UserStore, audit UserAuditWriter) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body createUserBody
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		email := normalizeEmail(body.Email)
		if email == "" || body.Password == "" {
			AbortProblem(c, http.StatusBadRequest, "bad request", "email and password are required")
			return
		}
		if !emailPattern.MatchString(email) {
			AbortProblem(c, http.StatusBadRequest, "bad request", "email is not a valid address")
			return
		}
		if len(body.Password) < minPasswordLength {
			AbortProblem(c, http.StatusBadRequest, "bad request",
				fmt.Sprintf("password must be at least %d characters", minPasswordLength))
			return
		}
		user, err := store.CreateUser(c.Request.Context(), tenantOf(c), email, body.Password, body.Role)
		if err != nil {
			handleUserWriteError(c, err)
			return
		}
		recordUserCreatedAudit(c, audit, user)
		c.JSON(http.StatusCreated, toUserDTO(user))
	}
}

// recordUserCreatedAudit writes a best-effort audit entry for a successful
// account creation; an audit failure must not fail the create the admin
// requested, so it is logged rather than surfaced.
func recordUserCreatedAudit(c *gin.Context, audit UserAuditWriter, u domain.User) {
	if audit == nil {
		return
	}
	actorID := ""
	if actor, ok := UserFromContext(c); ok {
		actorID = actor.ID
	}
	if err := audit.RecordUserCreatedAudit(c.Request.Context(), tenantOf(c), actorID, u.ID, u.Email, u.Role); err != nil {
		slog.Warn("recording user-created audit", "user", u.ID, "error", err)
	}
}

// handleUserWriteError maps a rejected create to the right status: a bad role (or
// other input the caller can fix) is a 400, and everything else flows through
// handleRepoError (duplicate email -> 409, cancellation -> 499, else 500).
func handleUserWriteError(c *gin.Context, err error) {
	if errors.Is(err, domain.ErrValidation) {
		AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
		return
	}
	handleRepoError(c, err)
}

// registerUsers mounts the admin create-user route when a store is wired. It is
// gated with the admin:tenant permission, matching how other privileged /api/v2
// mutations are gated (RequirePermission), so only administrators may create
// users. With no store it is left unregistered (unknown route -> 404).
func registerUsers(r gin.IRouter, store UserStore, audit UserAuditWriter) {
	if store == nil {
		return
	}
	r.POST("/api/v2/users", RequirePermission("admin", "tenant"), createUserHandler(store, audit))
}
