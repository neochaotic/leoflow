package oidc

import (
	"context"
	"errors"
	"fmt"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/neochaotic/leoflow/internal/config"
)

// StateCookieTTL bounds how long a login may sit at the IdP before the callback
// must arrive. Short enough to limit the replay window on the signed state
// cookie, long enough for a human to complete an MFA prompt.
const StateCookieTTL = 10 * time.Minute

// ErrNoIDToken is returned when the token endpoint's response carries no
// id_token — a misbehaving or misconfigured IdP. The login fails closed rather
// than proceeding without an identity to verify.
var ErrNoIDToken = errors.New("oidc: token response has no id_token")

// Flow drives the Authorization Code + PKCE login: it builds the authorization
// redirect, exchanges the code for tokens (using the client secret and the PKCE
// verifier), and exposes the keyless ID-token Verifier. Discovery runs once at
// construction and pins the issuer.
type Flow struct {
	oauth    *oauth2.Config
	verifier *Verifier
	codec    *StateCodec
	cfg      config.OIDCSection
}

// NewFlow discovers the issuer, pins it, and assembles the OAuth2 client and the
// ID-token verifier. hs256Secret is the app secret the state cookie is signed
// with (a derived key). It returns an error if discovery fails, so a
// misconfigured issuer is a boot failure rather than a per-login surprise.
func NewFlow(ctx context.Context, cfg config.OIDCSection, hs256Secret string) (*Flow, error) {
	provider, err := gooidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %q: %w", cfg.Issuer, err)
	}
	return &Flow{
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.RedirectURL,
			Scopes:       cfg.Scopes,
		},
		verifier: newVerifierWithProvider(provider, cfg),
		codec:    NewStateCodec(hs256Secret),
		cfg:      cfg,
	}, nil
}

// GenerateCodeVerifier returns a fresh PKCE code verifier (high-entropy,
// URL-safe). The caller stores it in the state cookie and passes it to Exchange.
func GenerateCodeVerifier() string { return oauth2.GenerateVerifier() }

// Codec returns the state-cookie codec so the caller can encode the cookie on
// login and decode it on callback.
func (f *Flow) Codec() *StateCodec { return f.codec }

// Verifier returns the ID-token verifier.
func (f *Flow) Verifier() *Verifier { return f.verifier }

// AuthCodeURL builds the authorization redirect for the given CSRF state, ID
// token nonce, and PKCE verifier. The verifier is sent as an S256 challenge
// (never in the clear); the nonce is echoed back in the ID token and checked on
// callback.
func (f *Flow) AuthCodeURL(state, nonce, verifier string) string {
	return f.oauth.AuthCodeURL(state,
		gooidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier),
	)
}

// Exchange trades the authorization code for tokens (proving possession of the
// PKCE verifier and the client secret) and returns the raw ID token for
// verification. A response without an id_token fails closed.
func (f *Flow) Exchange(ctx context.Context, code, verifier string) (string, error) {
	tok, err := f.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return "", fmt.Errorf("oidc: code exchange: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return "", ErrNoIDToken
	}
	return raw, nil
}
