package secretsource

// Backend is an operator-configured external secret store (set via Helm, never by
// a DAG author). It names which kinds it serves and the prefix under which a
// leoflow name is found in the provider. Keeping this operator-sourced is a
// security invariant: the ADR 0055 D6 registration check consults Covers to
// accept an externally-backed declared name without a provider call, so if a DAG
// could supply its own Backend it could mark any name "covered" and defeat D6.
type Backend struct {
	// Connections and Variables mark which kinds this backend serves. A prefix
	// alone does not enable a kind (a flat namespace has an empty prefix), and a
	// zero Backend serves nothing — an unconfigured operator never accepts an
	// undeclared external name at registration.
	Connections bool
	Variables   bool

	// ConnectionsPrefix and VariablesPrefix are the provider namespaces a leoflow
	// name is resolved under, mirroring Airflow's AWS backend kwargs. Empty means a
	// flat namespace (the name maps through unchanged).
	ConnectionsPrefix string
	VariablesPrefix   string
}

// Covers reports whether this backend serves the given kind — the ADR 0055 D6
// coverage predicate. It derives only from the operator-set enabled flags.
func (b Backend) Covers(kind Kind) bool {
	switch kind {
	case KindConnection:
		return b.Connections
	case KindVariable:
		return b.Variables
	default:
		return false
	}
}

// SecretID maps a leoflow-declared name to the provider secret id via the
// operator's prefix. The leoflow name never carries a provider path; the prefix
// convention lives here, in operator config.
func (b Backend) SecretID(name string, kind Kind) string {
	prefix := b.VariablesPrefix
	if kind == KindConnection {
		prefix = b.ConnectionsPrefix
	}
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
}
