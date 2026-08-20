package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// audienceAgent scopes tokens minted for the in-pod agent. A token with this
// audience identifies exactly one task instance and cannot be used on the
// user-facing API (see audienceUser).
const audienceAgent = "leoflow-agent"

// AgentIdentity is the task instance a verified agent token represents.
type AgentIdentity struct {
	TaskInstanceID string
	TenantID       string
	DagID          string
	RunID          string
	TaskID         string
	TryNumber      int
}

// agentClaims is the JWT payload of an agent identity token.
type agentClaims struct {
	TenantID  string `json:"tenant_id"`
	DagID     string `json:"dag_id"`
	RunID     string `json:"run_id"`
	TaskID    string `json:"task_id"`
	TryNumber int    `json:"try_number"`
	// OriginIssuedAt records when this attempt's credential was FIRST minted (at
	// dispatch). Unlike iat, it is preserved verbatim across heartbeat renewals so
	// the control plane can bound an attempt's total credential lifetime no matter
	// how many times its token is re-minted (ADR 0055 Fix #4). Absent on tokens
	// minted before this field existed; renewal then falls back to iat.
	OriginIssuedAt *jwt.NumericDate `json:"oiat,omitempty"`
	jwt.RegisteredClaims
}

// IssueAgentToken mints a signed token that identifies a single task instance,
// valid for the given TTL. The control plane passes it to the worker pod.
//
// The token's origin (oiat) is set to the mint time — this is a fresh dispatch.
// Renewal (RenewAgentToken) preserves that origin instead of resetting it.
func (a *JWTAuthenticator) IssueAgentToken(id AgentIdentity, ttl time.Duration) (string, error) {
	return a.mintAgentToken(id, ttl, a.clock())
}

// mintAgentToken signs an agent token whose iat/exp are anchored at now and whose
// preserved dispatch-origin is `origin`. Dispatch passes now as the origin; a
// renewal passes the incoming token's origin so it never advances.
func (a *JWTAuthenticator) mintAgentToken(id AgentIdentity, ttl time.Duration, origin time.Time) (string, error) {
	now := a.clock()
	c := agentClaims{
		TenantID:       id.TenantID,
		DagID:          id.DagID,
		RunID:          id.RunID,
		TaskID:         id.TaskID,
		TryNumber:      id.TryNumber,
		OriginIssuedAt: jwt.NewNumericDate(origin),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   id.TaskInstanceID,
			Issuer:    tokenIssuer,
			Audience:  jwt.ClaimStrings{audienceAgent},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(a.secret)
	if err != nil {
		return "", fmt.Errorf("signing agent token: %w", err)
	}
	return signed, nil
}

// RenewAgentToken validates an in-flight agent bearer token and re-mints it for
// the SAME task instance with a fresh short TTL, refreshing the live window
// without ever changing what AuthenticateAgent verifies (signature, issuer,
// leoflow-agent audience, HS256). It is the heartbeat-driven half of the
// short-TTL-plus-renewal design (ADR 0055 Fix #4): the short TTL bounds a
// stolen or finished token, while renewal keeps a genuinely live task's
// credential working.
//
// The attempt's original dispatch time is preserved across every renewal (the
// oiat claim). maxLifetime is a hard ceiling on that total age: once the attempt
// has been alive longer than maxLifetime since first dispatch, renewal is
// refused (ok=false, empty token) so a runaway attempt's credential lapses
// instead of being kept alive forever. A non-positive maxLifetime disables the
// ceiling. exp is always now+ttl — never accumulated onto the previous exp.
//
// An invalid incoming token (bad signature, wrong audience, expired) returns an
// error and is never re-minted.
func (a *JWTAuthenticator) RenewAgentToken(token string, ttl, maxLifetime time.Duration) (renewed string, ok bool, err error) {
	var c agentClaims
	parsed, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) {
		return a.secret, nil
	}, jwt.WithIssuer(tokenIssuer), jwt.WithAudience(audienceAgent), jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(a.clock))
	if err != nil || !parsed.Valid {
		return "", false, errors.Join(ErrInvalidToken, err)
	}
	// Preserve the attempt's first-dispatch origin across renewals; fall back to
	// iat for a token minted before the origin claim existed.
	origin := time.Time{}
	if c.OriginIssuedAt != nil {
		origin = c.OriginIssuedAt.Time
	} else if c.IssuedAt != nil {
		origin = c.IssuedAt.Time
	}
	if maxLifetime > 0 && !origin.IsZero() && a.clock().Sub(origin) > maxLifetime {
		return "", false, nil // past the ceiling: let the credential lapse
	}
	id := AgentIdentity{
		TaskInstanceID: c.Subject,
		TenantID:       c.TenantID,
		DagID:          c.DagID,
		RunID:          c.RunID,
		TaskID:         c.TaskID,
		TryNumber:      c.TryNumber,
	}
	renewed, err = a.mintAgentToken(id, ttl, origin)
	if err != nil {
		return "", false, err
	}
	return renewed, true, nil
}

// AuthenticateAgent validates an agent bearer token and returns the task
// instance it identifies.
func (a *JWTAuthenticator) AuthenticateAgent(token string) (*AgentIdentity, error) {
	var c agentClaims
	parsed, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) {
		return a.secret, nil
	}, jwt.WithIssuer(tokenIssuer), jwt.WithAudience(audienceAgent), jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(a.clock))
	if err != nil || !parsed.Valid {
		return nil, errors.Join(ErrInvalidToken, err)
	}
	return &AgentIdentity{
		TaskInstanceID: c.Subject,
		TenantID:       c.TenantID,
		DagID:          c.DagID,
		RunID:          c.RunID,
		TaskID:         c.TaskID,
		TryNumber:      c.TryNumber,
	}, nil
}
