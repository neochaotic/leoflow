package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/executor"
	"github.com/neochaotic/leoflow/internal/scheduler"
)

// TestResilienceLadderWiringValidates pins that the ladder the server actually
// boots with — agent heartbeat/TTL, default reaper config, reconcile interval,
// the scheduler's infra re-place ceiling and the default credential-lifetime
// ceiling — satisfies every ordering the restart recovery depends on. A change
// to any one of those constants that breaks the order fails this test before it
// fails a deployment at boot.
func TestResilienceLadderWiringValidates(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.Auth.MaxAttemptCredentialLifetime = 24 * time.Hour
	l := resilienceLadder(cfg)
	if err := executor.ValidateResilienceLadder(l); err != nil {
		t.Fatalf("server ladder %+v must validate: %v", l, err)
	}
	if l.ReconcileInterval != reconcileInterval || l.AttemptTokenTTL != attemptTokenTTL {
		t.Errorf("ladder must carry the wired values: %+v", l)
	}
	if l.InfraReplaceMaxDelay != scheduler.InfraReplaceMaxDelay() {
		t.Errorf("ladder must carry the scheduler's infra re-place ceiling %v, got %v", scheduler.InfraReplaceMaxDelay(), l.InfraReplaceMaxDelay)
	}
	if l.OrphanThreshold != executor.DefaultReaperConfig().OrphanThreshold {
		t.Errorf("ladder must carry the orphan threshold, got %v", l.OrphanThreshold)
	}
	if l.MaxAttemptCredentialLifetime != 24*time.Hour {
		t.Errorf("ladder must carry the configured credential ceiling, got %v", l.MaxAttemptCredentialLifetime)
	}
}

// TestResilienceLadderWiringFailsOnShortCredentialCeiling: the ONLY
// operator-tunable rung is auth.max_attempt_credential_lifetime. Hardening it
// below the per-attempt token TTL would silently disable heartbeat renewal — and
// with it the whole restart recovery — so boot must refuse, naming the key.
func TestResilienceLadderWiringFailsOnShortCredentialCeiling(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.Auth.MaxAttemptCredentialLifetime = 5 * time.Minute
	err := executor.ValidateResilienceLadder(resilienceLadder(cfg))
	if err == nil {
		t.Fatal("a 5-minute credential ceiling under a 10-minute token TTL must fail validation")
	}
	if !strings.Contains(err.Error(), "auth.max_attempt_credential_lifetime") {
		t.Errorf("error %q must name the config key the operator has to move", err)
	}
}

// TestResilienceLadderWiringWarnsWhenCredentialCeilingDisabled: a non-positive
// auth.max_attempt_credential_lifetime passes validation (it is the documented
// "no ceiling" setting) yet silently removes two backstops — unbounded heartbeat
// renewal and no activeDeadlineSeconds floor on task pods without a declared
// execution_timeout. The boot WARN is the operator's only signal, so the boot
// path must emit exactly one WARN naming the key when the ceiling is disabled,
// and none when it is set.
func TestResilienceLadderWiringWarnsWhenCredentialCeilingDisabled(t *testing.T) {
	warn := func(d time.Duration) string {
		var buf bytes.Buffer
		cfg := &config.ServerConfig{}
		cfg.Auth.MaxAttemptCredentialLifetime = d
		warnStartup(cfg, slog.New(slog.NewTextHandler(&buf, nil)))
		return buf.String()
	}
	for _, d := range []time.Duration{0, -time.Minute} {
		out := warn(d)
		if strings.Count(out, "level=WARN") != 1 {
			t.Errorf("ceiling %v: want exactly one WARN, got %q", d, out)
		}
		if !strings.Contains(out, "auth.max_attempt_credential_lifetime") || !strings.Contains(out, "activeDeadlineSeconds") {
			t.Errorf("ceiling %v: WARN must name the key and the lost pod deadline floor, got %q", d, out)
		}
	}
	if out := warn(24 * time.Hour); out != "" {
		t.Errorf("a set ceiling must log nothing at boot, got %q", out)
	}
}
