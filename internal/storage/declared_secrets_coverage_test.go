package storage

import (
	"reflect"
	"testing"
)

// unknownDeclaredNames is the pure core of the ADR 0055 D6 registration check,
// extended for ADR 0060 B1: a declared name is unknown only if it is neither in
// the vault (existing) nor covered by a configured external backend. A name
// covered by an operator-configured external backend is accepted at registration
// WITHOUT any provider call — its existence is proven pod-side at resolve time
// (fail-closed). This is what lets an externally-sourced DAG register.

func TestUnknownDeclaredNamesNoBackend(t *testing.T) {
	// covered=nil → pre-0060 behavior: a declared name absent from the vault is
	// unknown.
	got := unknownDeclaredNames([]string{"databricks", "warehouse"}, []string{"warehouse"}, nil)
	if want := []string{"databricks"}; !reflect.DeepEqual(got, want) {
		t.Errorf("unknownDeclaredNames = %v, want %v", got, want)
	}
}

func TestUnknownDeclaredNamesCoveredIsAccepted(t *testing.T) {
	// databricks is not in the vault but IS covered by a configured external
	// backend → not unknown, so registration must accept it.
	covered := func(name string) bool { return name == "databricks" }
	got := unknownDeclaredNames([]string{"databricks", "typo_conn"}, []string{}, covered)
	if want := []string{"typo_conn"}; !reflect.DeepEqual(got, want) {
		t.Errorf("unknownDeclaredNames with coverage = %v, want %v (covered name must be accepted, typo still rejected)", got, want)
	}
}

func TestUnknownDeclaredNamesVaultWinsOverCoverage(t *testing.T) {
	// A name present in the vault is known regardless of coverage — no double
	// counting, no error.
	covered := func(string) bool { return false }
	got := unknownDeclaredNames([]string{"warehouse"}, []string{"warehouse"}, covered)
	if len(got) != 0 {
		t.Errorf("unknownDeclaredNames = %v, want empty (vault name is known)", got)
	}
}
