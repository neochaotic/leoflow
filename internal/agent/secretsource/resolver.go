// Package secretsource resolves a leoflow-declared Connection/Variable name from
// an external secret store (e.g. AWS Secrets Manager), pod-side in the agent.
//
// The control plane never resolves an external secret: doing so would send an
// author-named reference over a core-identity network call, the confused-deputy
// class the pod boundary forecloses in advance. Resolution runs here, in the
// agent, under the pod's own workload identity. The resolver is only ever asked
// for names the DAG declared, so declaration remains the scope authority; the
// leoflow vault stays the chain fallback.
package secretsource

import "context"

// Kind distinguishes a connection (resolves to an Airflow connection URI) from a
// variable (resolves to a scalar value). The resolver learns only the
// leoflow-declared name and its kind — never a provider path, ARN, or mount.
type Kind int

const (
	// KindConnection resolves to a rendered Airflow connection URI.
	KindConnection Kind = iota
	// KindVariable resolves to a scalar variable value.
	KindVariable
)

// SecretResolver resolves one leoflow-declared name to its value using an
// identity the pod already carries. It never receives credentials, a provider
// path, or an ARN — those live in the adapter's operator-supplied config, bound
// to the pod's workload identity.
//
// Resolve returns (value, found, err). found=false is a clean miss: the caller
// falls through to the next link in the chain (the leoflow vault). A non-nil err
// is a hard failure (access denied, throttle, malformed value); the caller fails
// closed for a required name rather than masking it.
type SecretResolver interface {
	Resolve(ctx context.Context, name string, kind Kind) (value string, found bool, err error)
}

// Ref identifies one declared secret to resolve: its leoflow name and kind.
type Ref struct {
	Name string
	Kind Kind
}

// BatchResolver resolves many declared names in a single call. The 2b in-pod
// resolver pays a heavy per-invocation startup (a Python/Airflow subprocess), so
// it must batch rather than spawn once per name. A resolver may implement both
// interfaces; the chain prefers ResolveBatch when available. The returned map
// holds only the hits (a clean miss is an omission → the vault fallback stands);
// a non-nil error is a hard failure the caller fails closed on.
type BatchResolver interface {
	ResolveBatch(ctx context.Context, refs []Ref) (map[Ref]string, error)
}

// NoOp is the default resolver, used when no external backend is configured. It
// resolves nothing, so the chain falls through to the vault — behavior identical
// to the pre-ADR-0060 env-export path. A DAG that declares no external backend
// gets byte-identical secrets delivery.
type NoOp struct{}

// Resolve always reports a clean miss.
func (NoOp) Resolve(_ context.Context, _ string, _ Kind) (value string, found bool, err error) {
	return "", false, nil
}
