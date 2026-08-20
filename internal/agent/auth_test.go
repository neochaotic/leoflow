package agent

import (
	"context"
	"testing"
)

func TestTokenAuthAttachesBearer(t *testing.T) {
	creds := tokenAuth{source: NewTokenSource("abc123"), secure: true}
	md, err := creds.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetRequestMetadata: %v", err)
	}
	if md["authorization"] != "Bearer abc123" {
		t.Errorf("authorization = %q, want Bearer abc123", md["authorization"])
	}
	if !creds.RequireTransportSecurity() {
		t.Error("secure credentials must require transport security")
	}
}

func TestTokenAuthInsecureForLocalDev(t *testing.T) {
	creds := tokenAuth{source: NewTokenSource("x"), secure: false}
	if creds.RequireTransportSecurity() {
		t.Error("insecure credentials must not require transport security")
	}
}
