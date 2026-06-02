package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"
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
	t.Cleanup(resetGlobalTracerProvider)
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

// TestSetupOTelDisabledInstallsNoopProvider pins the fix for #319: when
// OTelEnabled=false the global tracer provider MUST be the no-op
// implementation. Without explicitly installing it, the global stays at
// whatever state the process happens to be in — e.g. a stray OTEL_EXPORTER_*
// env var auto-instantiating a real exporter, or test pollution from an
// earlier Setup(enabled=true) call. The downstream effect is that
// `otel.Tracer("leoflow")` in handler middleware (internal/api/observe.go)
// returns a real, recording tracer that tries to flush spans to
// localhost:4317 — the spam #319 reports.
func TestSetupOTelDisabledInstallsNoopProvider(t *testing.T) {
	t.Cleanup(resetGlobalTracerProvider)
	// Pollute the global with a real-ish provider first, simulating the
	// scenario where some other init path installed one before Setup runs.
	otel.SetTracerProvider(otel.GetTracerProvider())

	_, shutdown, err := Setup(context.Background(), Config{
		ServiceName: "leoflow-test",
		OTelEnabled: false,
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer shutdown()

	// The no-op tracer's span is non-recording. That is the documented
	// invariant of the no-op SDK and the simplest contract to assert.
	_, span := otel.GetTracerProvider().Tracer("test").Start(context.Background(), "probe")
	defer span.End()
	if span.IsRecording() {
		t.Errorf("expected disabled OTel to yield a non-recording span; got recording=true (provider=%T)", otel.GetTracerProvider())
	}
}

// TestSetupOTelEnabledWithEmptyEndpointRefuses pins the second leg of the
// fix: an `enabled=true` with no endpoint must NOT silently fall back to the
// otlptracegrpc default (`localhost:4317`), which is the exact behavior #319
// reports as a connection-refused log storm. The operator must configure the
// endpoint explicitly — empty endpoint with enabled=true is a misconfig and
// should fail loud at boot, not silently spam.
func TestSetupOTelEnabledWithEmptyEndpointRefuses(t *testing.T) {
	t.Cleanup(resetGlobalTracerProvider)

	_, _, err := Setup(context.Background(), Config{
		ServiceName:  "leoflow-test",
		OTelEnabled:  true,
		OTelEndpoint: "",
	})
	if err == nil {
		t.Fatal("expected an error when OTel is enabled without an endpoint, got nil")
	}
}

// resetGlobalTracerProvider reinstalls the otel default noop provider so a
// test that flips the global tracer provider doesn't leak state to the next
// test in the same package.
func resetGlobalTracerProvider() {
	otel.SetTracerProvider(noop.NewTracerProvider())
}
