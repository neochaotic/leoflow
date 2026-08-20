package storage

import (
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

// The agent-facing task spec carries the declared secret set (ADR 0045, ADR
// 0055) so a later increment can scope delivery to it. Resolution is
// inheritance: a task that narrows uses its own declaration, otherwise it
// inherits the DAG-level declaration. This carries data only — nothing here
// filters what secrets the agent receives.
func TestDeclaredSecretResolution(t *testing.T) {
	cases := []struct {
		name     string
		task     domain.TaskSpec
		spec     domain.DAGSpec
		wantVars []string
		wantConn []string
	}{
		{
			name:     "inherits dag level when task declares nothing",
			task:     domain.TaskSpec{},
			spec:     domain.DAGSpec{Variables: []string{"greeting"}, Connections: []string{"warehouse"}},
			wantVars: []string{"greeting"},
			wantConn: []string{"warehouse"},
		},
		{
			name:     "task narrows the dag declaration",
			task:     domain.TaskSpec{Variables: []string{"greeting"}, Connections: []string{"warehouse"}},
			spec:     domain.DAGSpec{Variables: []string{"greeting", "farewell"}, Connections: []string{"warehouse", "lake"}},
			wantVars: []string{"greeting"},
			wantConn: []string{"warehouse"},
		},
		{
			name:     "empty when neither declares",
			task:     domain.TaskSpec{},
			spec:     domain.DAGSpec{},
			wantVars: nil,
			wantConn: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := declaredVariables(tc.task, tc.spec); !equalStrings(got, tc.wantVars) {
				t.Errorf("declaredVariables = %v, want %v", got, tc.wantVars)
			}
			if got := declaredConnections(tc.task, tc.spec); !equalStrings(got, tc.wantConn) {
				t.Errorf("declaredConnections = %v, want %v", got, tc.wantConn)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
