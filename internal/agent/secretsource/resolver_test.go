package secretsource

import (
	"context"
	"testing"
)

// The NoOp resolver is the default when no external backend is configured. It
// must resolve nothing (found=false, no error) so the resolution chain falls
// through to the leoflow vault — byte-identical to pre-ADR-0060 behavior.
func TestNoOpResolvesNothing(t *testing.T) {
	var r SecretResolver = NoOp{}
	for _, kind := range []Kind{KindConnection, KindVariable} {
		value, found, err := r.Resolve(context.Background(), "databricks", kind)
		if err != nil {
			t.Fatalf("NoOp.Resolve(kind=%d) err = %v, want nil", kind, err)
		}
		if found {
			t.Errorf("NoOp.Resolve(kind=%d) found = true, want false", kind)
		}
		if value != "" {
			t.Errorf("NoOp.Resolve(kind=%d) value = %q, want empty", kind, value)
		}
	}
}

// KindConnection and KindVariable must be distinct so the resolver can tell a
// connection (resolves to an Airflow URI) from a variable (a scalar).
func TestKindsAreDistinct(t *testing.T) {
	if KindConnection == KindVariable {
		t.Fatal("KindConnection and KindVariable must be distinct values")
	}
}
