package oidc

import (
	"reflect"
	"testing"
)

func TestMapRolesDefaultDeny(t *testing.T) {
	mappings := map[string]string{"platform-admins": "admin", "data-eng": "editor"}
	for _, tc := range []struct {
		name   string
		groups []string
		want   []string
	}{
		{"mapped groups map to roles", []string{"data-eng"}, []string{"editor"}},
		{"unmapped group grants nothing", []string{"random-group"}, nil},
		{"mix keeps only mapped", []string{"random-group", "platform-admins"}, []string{"admin"}},
		{"no groups grants nothing", nil, nil},
		{"duplicate role de-duplicated", []string{"data-eng", "data-eng"}, []string{"editor"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := MapRoles(tc.groups, mappings)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("MapRoles(%v) = %v, want %v", tc.groups, got, tc.want)
			}
		})
	}
}

func TestMapRolesEmptyMappingGrantsNothing(t *testing.T) {
	if got := MapRoles([]string{"anything"}, nil); got != nil {
		t.Errorf("MapRoles with no mapping = %v, want nil (default-deny)", got)
	}
}

func TestApplyDefaultRole(t *testing.T) {
	for _, tc := range []struct {
		name        string
		roles       []string
		defaultRole string
		want        []string
	}{
		{"empty + default set → fallback", nil, "viewer", []string{"viewer"}},
		{"empty + no default → still empty (secure default)", nil, "", nil},
		{"non-empty is never overridden", []string{"editor"}, "viewer", []string{"editor"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ApplyDefaultRole(tc.roles, tc.defaultRole)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ApplyDefaultRole(%v, %q) = %v, want %v", tc.roles, tc.defaultRole, got, tc.want)
			}
		})
	}
}
