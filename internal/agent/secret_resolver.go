package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/neochaotic/leoflow/internal/agent/secretsource"
)

// resolverPython / resolverModule are the in-pod entrypoint the agent drives to
// resolve external secrets (2b, ADR 0060): a small Python module that instantiates
// the operator-configured provider secrets backend and resolves the declared
// names under the pod's own workload identity. The agent links no cloud SDK.
const (
	resolverPython = "python3"
	resolverModule = "leoflow_runtime.resolve_secrets"
)

// resolverInput is the JSON the agent writes to the resolver subprocess's STDIN
// (never argv/env: a shared PID namespace makes those readable by the task).
type resolverInput struct {
	Backend       string          `json:"backend"`
	BackendKwargs json.RawMessage `json:"backend_kwargs"`
	Connections   []string        `json:"connections"`
	Variables     []string        `json:"variables"`
}

// resolverOutput is the JSON the subprocess writes to STDOUT: only hits are
// present (a miss is an omission → the vault fallback stands). Connections are
// rendered Airflow connection URIs (so bash tasks reading $AIRFLOW_CONN_* work).
type resolverOutput struct {
	Connections map[string]string `json:"connections"`
	Variables   map[string]string `json:"variables"`
}

// subprocessResolver resolves declared secrets by driving the in-pod Python
// backend (2b). It implements secretsource.BatchResolver (one subprocess for all
// declared names) and the per-name SecretResolver. Config travels on stdin; the
// subprocess runs synchronously (gone before the task starts) with a base env
// scrubbed of the agent's own LEOFLOW_ secrets.
type subprocessResolver struct {
	cfg secretsource.BackendConfig
	// run executes the resolver with the given stdin JSON and returns its stdout.
	// Injectable for tests; nil uses spawn (the real subprocess).
	run func(ctx context.Context, stdin []byte) (stdout []byte, err error)
}

// newSubprocessResolver builds the 2b resolver from the operator backend config.
func newSubprocessResolver(cfg secretsource.BackendConfig) *subprocessResolver {
	r := &subprocessResolver{cfg: cfg}
	r.run = r.spawn
	return r
}

// ResolverFromEnv builds the external-secrets resolver + its routing Backend from
// the operator-injected LEOFLOW_SECRETS_* pod env (operator-only: the dispatch
// filter, #828, keeps an author's task env from setting LEOFLOW_ keys). It returns
// a nil resolver when no backend is configured — the chain then stays vault-only,
// byte-identical to the pre-0060 env-export. A malformed config fails closed
// (returned error) rather than silently disabling external secrets.
func ResolverFromEnv(getenv func(string) string) (secretsource.SecretResolver, secretsource.Backend, error) {
	cfg, enabled, err := secretsource.ParseBackendConfig(
		getenv("LEOFLOW_SECRETS_BACKEND"), getenv("LEOFLOW_SECRETS_BACKEND_KWARGS"))
	if err != nil {
		return nil, secretsource.Backend{}, err
	}
	if !enabled {
		return nil, secretsource.Backend{}, nil
	}
	return newSubprocessResolver(cfg), cfg.Routing, nil
}

// Resolve satisfies the per-name port by batching a single name.
func (r *subprocessResolver) Resolve(ctx context.Context, name string, kind secretsource.Kind) (value string, found bool, err error) {
	res, rerr := r.ResolveBatch(ctx, []secretsource.Ref{{Name: name, Kind: kind}})
	if rerr != nil {
		return "", false, rerr
	}
	v, ok := res[secretsource.Ref{Name: name, Kind: kind}]
	return v, ok, nil
}

// ResolveBatch resolves all refs in one subprocess call. A hard failure (spawn
// error, non-zero exit, malformed output) is returned as a SANITIZED error — the
// raw stderr/err is never surfaced (a provider backend can echo the secret) — so
// the caller fails the task closed without leaking (ADR 0060 B6).
func (r *subprocessResolver) ResolveBatch(ctx context.Context, refs []secretsource.Ref) (map[secretsource.Ref]string, error) {
	in := resolverInput{Backend: r.cfg.Class, BackendKwargs: r.cfg.Kwargs}
	for _, ref := range refs {
		if ref.Kind == secretsource.KindConnection {
			in.Connections = append(in.Connections, ref.Name)
		} else {
			in.Variables = append(in.Variables, ref.Name)
		}
	}
	stdin, merr := json.Marshal(in)
	if merr != nil {
		return nil, errors.New("external secret resolver: encoding request")
	}
	stdout, rerr := r.run(ctx, stdin)
	if rerr != nil {
		slog.Debug("external secret resolver failed", "error", rerr)
		return nil, errors.New("external secret resolver failed")
	}
	var out resolverOutput
	if uerr := json.Unmarshal(stdout, &out); uerr != nil {
		slog.Debug("external secret resolver: malformed output", "error", uerr)
		return nil, errors.New("external secret resolver returned malformed output")
	}
	result := make(map[secretsource.Ref]string, len(out.Connections)+len(out.Variables))
	for name, uri := range out.Connections {
		result[secretsource.Ref{Name: name, Kind: secretsource.KindConnection}] = uri
	}
	for name, val := range out.Variables {
		result[secretsource.Ref{Name: name, Kind: secretsource.KindVariable}] = val
	}
	return result, nil
}

// resolverCredOverrideEnv are provider env vars an author could set in their task
// env (they are not LEOFLOW_-prefixed, so #828's dispatch filter lets them through
// into the pod env) that would divert the resolver SDK's credential chain or
// endpoint away from the pod's own keyless workload identity — static creds, a
// credentials file/profile, or an endpoint override. The resolver must authenticate
// ONLY as the pod (IRSA / Workload Identity / Vault k8s-auth), so these are scrubbed
// from its base env. The keyless vars the provider webhook injects (AWS_ROLE_ARN,
// AWS_WEB_IDENTITY_TOKEN_FILE, AWS_CONTAINER_*, the GKE metadata server, the Azure
// federated token) are NOT in this list and survive. (Real credential precedence
// is a NEEDS-REAL-CLUSTER check on the EKS RC.)
var resolverCredOverrideEnv = map[string]bool{
	"AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true,
	"AWS_PROFILE": true, "AWS_SHARED_CREDENTIALS_FILE": true, "AWS_CONFIG_FILE": true,
	"GOOGLE_APPLICATION_CREDENTIALS": true,
	"AZURE_CLIENT_SECRET":            true, "AZURE_CLIENT_CERTIFICATE_PATH": true,
}

// resolverBaseEnv is the subprocess's base env: the agent's env minus its own
// LEOFLOW_ secrets (stripAgentOnly) and minus any author-set provider credential /
// endpoint override (so the resolver authenticates only as the pod, keyless).
func resolverBaseEnv(agentEnv []string) []string {
	stripped := stripAgentOnly(agentEnv)
	out := stripped[:0:0]
	for _, kv := range stripped {
		name, _, _ := strings.Cut(kv, "=")
		if resolverCredOverrideEnv[name] || strings.HasPrefix(name, "AWS_ENDPOINT_URL") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// spawn runs the Python resolver synchronously with config on stdin and a base env
// scrubbed of the agent's LEOFLOW_ secrets and any author-set provider credential /
// endpoint override (resolverBaseEnv), so it authenticates only as the pod's keyless
// identity. Its stderr goes to the agent debug log only — never the task log sink —
// since a backend may echo secret material there. It completes before the task
// process is ever started.
func (r *subprocessResolver) spawn(ctx context.Context, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, resolverPython, "-m", resolverModule)
	cmd.Env = resolverBaseEnv(os.Environ())
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if stderr.Len() > 0 {
		slog.Debug("external secret resolver stderr", "output", stderr.String())
	}
	if err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}
