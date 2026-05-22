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
	jwt.RegisteredClaims
}

// IssueAgentToken mints a signed token that identifies a single task instance,
// valid for the given TTL. The control plane passes it to the worker pod.
func (a *JWTAuthenticator) IssueAgentToken(id AgentIdentity, ttl time.Duration) (string, error) {
	now := time.Now()
	c := agentClaims{
		TenantID:  id.TenantID,
		DagID:     id.DagID,
		RunID:     id.RunID,
		TaskID:    id.TaskID,
		TryNumber: id.TryNumber,
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

// AuthenticateAgent validates an agent bearer token and returns the task
// instance it identifies.
func (a *JWTAuthenticator) AuthenticateAgent(token string) (*AgentIdentity, error) {
	var c agentClaims
	parsed, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) {
		return a.secret, nil
	}, jwt.WithIssuer(tokenIssuer), jwt.WithAudience(audienceAgent), jwt.WithValidMethods([]string{"HS256"}))
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
