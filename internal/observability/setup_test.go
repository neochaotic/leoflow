package observability

import (
	"context"
	"testing"
)

func TestSetupDisabledOTel(t *testing.T) {
	tel, shutdown, err := Setup(context.Background(), Config{
		ServiceName: "leoflow-test",
		LogLevel:    "info",
		LogFormat:   "json",
		OTelEnabled: false,
	})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	defer shutdown()

	if tel.Logger == nil || tel.Metrics == nil || tel.Registry == nil || tel.Tracer == nil {
		t.Fatal("all telemetry fields must be non-nil")
	}
	if _, err := tel.Registry.Gather(); err != nil {
		t.Errorf("registry gather: %v", err)
	}
}

func TestSetupEnabledOTelDoesNotBlock(t *testing.T) {
	tel, shutdown, err := Setup(context.Background(), Config{
		ServiceName:  "leoflow-test",
		LogLevel:     "info",
		LogFormat:    "json",
		OTelEnabled:  true,
		OTelEndpoint: "localhost:4317",
	})
	if err != nil {
		t.Fatalf("Setup() with OTel enabled error = %v", err)
	}
	if tel == nil {
		t.Fatal("nil telemetry")
	}
	shutdown()
}
