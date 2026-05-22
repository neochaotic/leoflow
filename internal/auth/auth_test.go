package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeStore struct {
	user *User
	hash string
	err  error
}

func (f *fakeStore) FindUserByLogin(_ context.Context, _, _ string) (*User, string, error) {
	return f.user, f.hash, f.err
}

func must(s string, err error) string {
	if err != nil {
		panic(err)
	}
	return s
}

func TestHasPermission(t *testing.T) {
	if !(&User{Roles: []string{"admin"}}).HasPermission("write", "dag") {
		t.Error("admin role should grant everything")
	}
	reader := &User{Permissions: []Permission{{Action: "read", Resource: "dag"}}}
	if !reader.HasPermission("read", "dag") {
		t.Error("reader should have read:dag")
	}
	if reader.HasPermission("write", "dag") {
		t.Error("reader should not have write:dag")
	}
	if !(&User{Permissions: []Permission{{Action: "admin", Resource: "*"}}}).HasPermission("execute", "dag") {
		t.Error("admin:* should grant everything")
	}
}

func TestPasswordHashVerify(t *testing.T) {
	h, err := HashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(h, "s3cret") {
		t.Error("correct password should verify")
	}
	if VerifyPassword(h, "wrong") {
		t.Error("wrong password should not verify")
	}
}

func TestJWTIssueAndAuthenticate(t *testing.T) {
	store := &fakeStore{user: &User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}, hash: must(HashPassword("pw"))}
	a := NewJWTAuthenticator(store, "secret", time.Hour)
	tok, err := a.IssueToken(context.Background(), Credentials{Tenant: "default", Username: "a@b.c", Password: "pw"})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	u, err := a.Authenticate(context.Background(), tok)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if u.ID != "u1" || u.TenantID != "default" || len(u.Roles) != 1 || u.Roles[0] != "admin" {
		t.Errorf("unexpected user: %+v", u)
	}
}

func TestJWTRejectsBadPassword(t *testing.T) {
	store := &fakeStore{user: &User{ID: "u1"}, hash: must(HashPassword("pw"))}
	a := NewJWTAuthenticator(store, "secret", time.Hour)
	if _, err := a.IssueToken(context.Background(), Credentials{Password: "nope"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("want ErrInvalidCredentials, got %v", err)
	}
}

func TestJWTRejectsWrongSecretAndTampered(t *testing.T) {
	store := &fakeStore{user: &User{ID: "u1", Roles: []string{"admin"}}, hash: must(HashPassword("pw"))}
	a := NewJWTAuthenticator(store, "secret", time.Hour)
	tok, _ := a.IssueToken(context.Background(), Credentials{Password: "pw"})

	other := NewJWTAuthenticator(store, "other-secret", time.Hour)
	if _, err := other.Authenticate(context.Background(), tok); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("wrong secret should yield ErrInvalidToken, got %v", err)
	}
	if _, err := a.Authenticate(context.Background(), tok+"tampered"); err == nil {
		t.Error("tampered token should fail")
	}
}

func TestJWTRejectsExpired(t *testing.T) {
	store := &fakeStore{user: &User{ID: "u1"}, hash: must(HashPassword("pw"))}
	a := NewJWTAuthenticator(store, "secret", -time.Hour)
	tok, _ := a.IssueToken(context.Background(), Credentials{Password: "pw"})
	if _, err := a.Authenticate(context.Background(), tok); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expired token should yield ErrInvalidToken, got %v", err)
	}
}

func TestRateLimiter(t *testing.T) {
	now := time.Now()
	r := NewRateLimiter(3, time.Minute)
	r.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if !r.Allow("ip") {
			t.Errorf("event %d should be allowed", i)
		}
	}
	if r.Allow("ip") {
		t.Error("4th event in window should be denied")
	}
	if !r.Allow("other-ip") {
		t.Error("a different key should be allowed")
	}
	now = now.Add(time.Minute + time.Second)
	if !r.Allow("ip") {
		t.Error("after the window resets, should be allowed again")
	}
}
