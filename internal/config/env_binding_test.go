package config

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// bindableEnvVars returns the set of LEOFLOW_* environment-variable names that
// LoadServer can bind, derived from serverDefaults. viper's AutomaticEnv only
// binds an env var for a key it has seen via SetDefault (see server.go), so the
// registered defaults are the single source of truth for what binds. The env
// name for a key is LEOFLOW_ + the key uppercased with '.' replaced by '_'.
func bindableEnvVars() map[string]struct{} {
	out := make(map[string]struct{}, len(serverDefaults))
	for key := range serverDefaults {
		env := "LEOFLOW_" + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
		out[env] = struct{}{}
	}
	return out
}

// docEnvVarsFromReference parses the configuration reference and returns every
// LEOFLOW_* variable named in the Variable column of a settings table. Rows that
// describe a config-file/Helm-values-only key spell it in dotted form
// (executor.defaults.staging_size) precisely because its env var does not bind,
// so those are correctly excluded. Only the first table column is scanned, so
// LEOFLOW_* tokens that appear in prose (e.g. LEOFLOW_AGENT_TOKEN, an agent-side
// credential) are not treated as server settings.
func docEnvVarsFromReference(t *testing.T) []string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	docPath := filepath.Join(filepath.Dir(thisFile), "..", "..",
		"website", "content", "reference", "configuration.md")
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}
	envRe := regexp.MustCompile(`LEOFLOW_[A-Z0-9_]+`)
	seen := map[string]struct{}{}
	var vars []string
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		// First column only: text up to the first column separator.
		firstCol := line
		if i := strings.Index(line[1:], "|"); i >= 0 {
			firstCol = line[:i+1]
		}
		for _, v := range envRe.FindAllString(firstCol, -1) {
			if _, dup := seen[v]; dup {
				continue
			}
			seen[v] = struct{}{}
			vars = append(vars, v)
		}
	}
	if len(vars) == 0 {
		t.Fatalf("no LEOFLOW_* variables parsed from %s", docPath)
	}
	return vars
}

// TestDocumentedEnvVarsBind is the class-level guard: every LEOFLOW_* variable
// documented as a server setting must be registered in serverDefaults, otherwise
// AutomaticEnv silently drops it — the failure mode of issue #725, where a Helm
// install (env-only override path) could not set server.trusted_proxies or the
// executor resource defaults.
func TestDocumentedEnvVarsBind(t *testing.T) {
	bindable := bindableEnvVars()
	for _, env := range docEnvVarsFromReference(t) {
		if _, ok := bindable[env]; !ok {
			t.Errorf("%s is documented as a server setting but is not registered "+
				"in serverDefaults, so viper's AutomaticEnv will not bind it "+
				"(Helm/env override silently ignored)", env)
		}
	}
}

// TestLoadServerBindsTrustedProxies proves the []string leaf binds from a single
// comma-separated env var — the only override path a Helm install has, since the
// chart passes no server config file. Consequence when unbound (#725): the login
// limiter keys on the ingress IP for every request, so a handful of bad logins
// locks out the whole deployment.
func TestLoadServerBindsTrustedProxies(t *testing.T) {
	t.Setenv("LEOFLOW_SERVER_TRUSTED_PROXIES", "10.0.0.0/8,192.168.1.1")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	want := []string{"10.0.0.0/8", "192.168.1.1"}
	if len(c.Server.TrustedProxies) != len(want) {
		t.Fatalf("TrustedProxies = %#v, want %#v", c.Server.TrustedProxies, want)
	}
	for i := range want {
		if c.Server.TrustedProxies[i] != want[i] {
			t.Fatalf("TrustedProxies = %#v, want %#v", c.Server.TrustedProxies, want)
		}
	}
}

// TestLoadServerBindsExecutorDefaultResources proves the L0 resource defaults
// bind from env. Consequence when unbound (#725): d.Resources stays nil, so
// tasks that rely on the cluster default get no requests/limits and land in
// BestEffort QoS (first evicted under node pressure).
func TestLoadServerBindsExecutorDefaultResources(t *testing.T) {
	t.Setenv("LEOFLOW_EXECUTOR_DEFAULTS_RESOURCES_CPU", "250m")
	t.Setenv("LEOFLOW_EXECUTOR_DEFAULTS_RESOURCES_MEMORY", "256Mi")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Executor.Defaults.ResourcesCPU != "250m" {
		t.Errorf("ResourcesCPU = %q, want %q", c.Executor.Defaults.ResourcesCPU, "250m")
	}
	if c.Executor.Defaults.ResourcesMemory != "256Mi" {
		t.Errorf("ResourcesMemory = %q, want %q", c.Executor.Defaults.ResourcesMemory, "256Mi")
	}
}

// TestLoadServerBindsExecutorDefaultStaging proves the L0 staging-volume defaults
// bind from env. Consequence when unbound (#743, same class as #725): a Helm
// install has no reachable route to set them — the chart mounts no server config
// file, so the documented "config file / Helm values" path does not exist and the
// env var was never registered — leaving the per-run staging PVC to fall back to
// the cluster's default StorageClass and an unset size.
func TestLoadServerBindsExecutorDefaultStaging(t *testing.T) {
	t.Setenv("LEOFLOW_EXECUTOR_DEFAULTS_STAGING_SIZE", "10Gi")
	t.Setenv("LEOFLOW_EXECUTOR_DEFAULTS_STAGING_STORAGE_CLASS", "efs-rwx")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Executor.Defaults.StagingSize != "10Gi" {
		t.Errorf("StagingSize = %q, want %q", c.Executor.Defaults.StagingSize, "10Gi")
	}
	if c.Executor.Defaults.StagingStorageClass != "efs-rwx" {
		t.Errorf("StagingStorageClass = %q, want %q", c.Executor.Defaults.StagingStorageClass, "efs-rwx")
	}
}

// TestLoadServerBindsRedisCAFile guards the same class for managed-Redis TLS:
// the chart sets LEOFLOW_REDIS_CA_FILE when redis.caConfigMap is configured, so
// it must bind or verified TLS to Memorystore/ElastiCache silently falls back to
// system roots.
func TestLoadServerBindsRedisCAFile(t *testing.T) {
	t.Setenv("LEOFLOW_REDIS_CA_FILE", "/etc/leoflow/redis-ca/ca.crt")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Redis.CAFile != "/etc/leoflow/redis-ca/ca.crt" {
		t.Errorf("Redis.CAFile = %q, want %q", c.Redis.CAFile, "/etc/leoflow/redis-ca/ca.crt")
	}
}
