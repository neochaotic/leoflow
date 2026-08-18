package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neochaotic/leoflow/internal/auth"
	"github.com/neochaotic/leoflow/internal/domain"
)

// fakeUserStore records the arguments it received so the handler tests can assert
// that the plaintext password is delegated to the store (which hashes it) and
// never echoed back in the response.
type fakeUserStore struct {
	created                        domain.User
	err                            error
	called                         bool
	gotEmail, gotPassword, gotRole string
}

func (f *fakeUserStore) CreateUser(_ context.Context, _, email, password, role string) (domain.User, error) {
	f.called = true
	f.gotEmail, f.gotPassword, f.gotRole = email, password, role
	if f.err != nil {
		return domain.User{}, f.err
	}
	return f.created, nil
}

func userServer(store UserStore, user *auth.User) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: user},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Users:         store,
	})
}

func adminUser() *auth.User {
	return &auth.User{ID: "admin-1", TenantID: "default", Roles: []string{"admin"}}
}

func TestCreateUserReturnsCreatedUserWithoutSecret(t *testing.T) {
	store := &fakeUserStore{created: domain.User{
		ID: "u-1", Email: "alice@example.com", Role: "admin", IsActive: true,
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}}
	srv := userServer(store, adminUser())

	body := `{"email":"alice@example.com","password":"s3cret-pass","role":"admin"}`
	rec := authGet(srv, http.MethodPost, "/api/v2/users", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", rec.Code, rec.Body.String())
	}
	// The store received the plaintext password (to hash), but the response must
	// never echo it or any hash.
	if store.gotPassword != "s3cret-pass" {
		t.Errorf("store should receive the plaintext password, got %q", store.gotPassword)
	}
	if strings.Contains(rec.Body.String(), "s3cret-pass") ||
		strings.Contains(rec.Body.String(), "password") ||
		strings.Contains(rec.Body.String(), "hash") {
		t.Errorf("response leaked a secret: %s", rec.Body.String())
	}
	var dto userDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.ID != "u-1" || dto.Email != "alice@example.com" || dto.Role != "admin" || !dto.IsActive {
		t.Errorf("unexpected user dto: %+v", dto)
	}
	if dto.CreatedAt != "2026-01-02T03:04:05Z" {
		t.Errorf("created_at = %q, want RFC3339", dto.CreatedAt)
	}
}

func TestCreateUserDuplicateEmailConflict(t *testing.T) {
	store := &fakeUserStore{err: domain.ErrConflict}
	rec := authGet(userServer(store, adminUser()), http.MethodPost, "/api/v2/users",
		`{"email":"dupe@example.com","password":"pw-12345678"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate email = %d, want 409", rec.Code)
	}
}

func TestCreateUserUnknownRoleIsBadRequest(t *testing.T) {
	store := &fakeUserStore{err: domain.ErrValidation}
	rec := authGet(userServer(store, adminUser()), http.MethodPost, "/api/v2/users",
		`{"email":"bob@example.com","password":"pw-12345678","role":"wizard"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown role = %d, want 400", rec.Code)
	}
}

func TestCreateUserRequiresAdmin(t *testing.T) {
	// A non-admin principal (no admin role, no admin permission) is forbidden.
	viewer := &auth.User{ID: "v-1", TenantID: "default", Roles: []string{"viewer"}}
	store := &fakeUserStore{}
	rec := authGet(userServer(store, viewer), http.MethodPost, "/api/v2/users",
		`{"email":"x@example.com","password":"pw-12345678"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("non-admin create = %d, want 403", rec.Code)
	}
	if store.called {
		t.Error("store must not be reached when the caller lacks permission")
	}
}

func TestCreateUserValidatesRequiredFields(t *testing.T) {
	cases := map[string]string{
		"missing email":    `{"password":"pw-12345678"}`,
		"missing password": `{"email":"a@example.com"}`,
		"short password":   `{"email":"a@example.com","password":"short"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fakeUserStore{}
			rec := authGet(userServer(store, adminUser()), http.MethodPost, "/api/v2/users", body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s = %d, want 400", name, rec.Code)
			}
			if store.called {
				t.Errorf("%s: store must not be reached on invalid input", name)
			}
		})
	}
}

func TestCreateUserWithoutStoreNotRegistered(t *testing.T) {
	// With no user store wired, the route is absent (404), never a 500.
	rec := authGet(userServer(nil, adminUser()), http.MethodPost, "/api/v2/users",
		`{"email":"a@example.com","password":"pw-12345678"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("nil store = %d, want 404", rec.Code)
	}
}
