package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/domain"
)

// minPasswordLength is the minimum length the create-user API accepts. It is a
// deliberately conservative floor, not a full password policy — the maintainer
// may tighten it later without breaking the endpoint's contract.
const minPasswordLength = 8

// UserStore creates control-plane accounts for the admin create-user API. The
// store hashes the plaintext password (reusing the bootstrap admin's bcrypt
// scheme) and returns the created user without any secret. A duplicate email
// must surface as domain.ErrConflict and an unknown role as domain.ErrValidation.
type UserStore interface {
	CreateUser(ctx context.Context, tenant, email, password, role string) (domain.User, error)
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

func createUserHandler(store UserStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body createUserBody
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		if body.Email == "" || body.Password == "" {
			AbortProblem(c, http.StatusBadRequest, "bad request", "email and password are required")
			return
		}
		if len(body.Password) < minPasswordLength {
			AbortProblem(c, http.StatusBadRequest, "bad request",
				fmt.Sprintf("password must be at least %d characters", minPasswordLength))
			return
		}
		user, err := store.CreateUser(c.Request.Context(), tenantOf(c), body.Email, body.Password, body.Role)
		if err != nil {
			handleUserWriteError(c, err)
			return
		}
		c.JSON(http.StatusCreated, toUserDTO(user))
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
func registerUsers(r gin.IRouter, store UserStore) {
	if store == nil {
		return
	}
	r.POST("/api/v2/users", RequirePermission("admin", "tenant"), createUserHandler(store))
}
