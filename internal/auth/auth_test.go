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
	store := &fakeStore{user: &User{ID: "u1", TenantID: "default", Email: "a@b.c", Roles: []string{"admin"}}, hash: must(HashPassword("pw"))}
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
	// Email must survive the round-trip, or /ui/auth/me shows a blank username.
	if u.Email != "a@b.c" {
		t.Errorf("email not preserved through token: %q", u.Email)
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

func TestRateLimiterBlockedIsAPeek(t *testing.T) {
	now := time.Now()
	r := NewRateLimiter(2, time.Minute)
	r.now = func() time.Time { return now }
	// Blocked must NOT consume budget: peeking any number of times keeps the key
	// usable. This lets the handler reject over-limit callers up front while only
	// counting actual failures.
	for i := 0; i < 10; i++ {
		if r.Blocked("ip") {
			t.Fatal("peek must not count toward the limit")
		}
	}
	r.Allow("ip")
	if r.Blocked("ip") {
		t.Error("one recorded event must not block (limit 2)")
	}
	r.Allow("ip")
	if !r.Blocked("ip") {
		t.Error("reaching the limit must block")
	}
	// The window resets the block.
	now = now.Add(time.Minute + time.Second)
	if r.Blocked("ip") {
		t.Error("a new window must not be blocked")
	}
}

func TestMintUserTokenRoundTrips(t *testing.T) {
	const secret = "dev-insecure-jwt-secret-change-me"
	token, err := MintUserToken(secret, time.Hour, User{
		ID: "dev", TenantID: "default", Email: "admin@leoflow.local", Roles: []string{"admin"},
	})
	if err != nil {
		t.Fatalf("MintUserToken: %v", err)
	}
	a := NewJWTAuthenticator(nil, secret, time.Hour)
	u, aerr := a.Authenticate(context.Background(), token)
	if aerr != nil {
		t.Fatalf("Authenticate(minted) = %v", aerr)
	}
	if u.TenantID != "default" || u.Email != "admin@leoflow.local" || len(u.Roles) != 1 || u.Roles[0] != "admin" {
		t.Errorf("round-trip user = %+v, want admin@default", u)
	}
	// A token signed with a different secret must be rejected.
	if _, e := a.Authenticate(context.Background(), func() string { s, _ := MintUserToken("other", time.Hour, User{ID: "x"}); return s }()); e == nil {
		t.Error("token signed with a different secret must not authenticate")
	}
}
