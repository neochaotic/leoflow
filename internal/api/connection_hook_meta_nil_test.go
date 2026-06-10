package api

import "testing"

// TestConnectionHookMetaStandardFieldsComplete guards the reported connector-config
// crash. The Airflow 3.2 FlexibleForm's LDt helper reads g1(spec.description),
// g1(spec.host), … g1(spec.url_schema) off EVERY connector. g1 tolerates a null
// value but not a missing key — an absent key is undefined and `undefined.hidden`
// crashed the whole Connections page ("Cannot read properties of undefined
// (reading 'hidden')"). The served catalog must therefore carry all six standard
// keys for every connector. (Captured via a headless-browser repro before the fix.)
func TestConnectionHookMetaStandardFieldsComplete(t *testing.T) {
	cat := connectionTypeCatalog()
	if len(cat) == 0 {
		t.Fatal("connection catalog is empty")
	}
	for _, e := range cat {
		for _, k := range standardFieldKeys {
			if _, ok := e.StandardFields[k]; !ok {
				t.Errorf("%s: standard_fields missing %q — the SPA's g1 reads it and crashes on undefined",
					e.ConnectionType, k)
			}
		}
		// Extra fields are rendered without g1's null guard, so a nil value there
		// would crash too. They must never be nil.
		for name, v := range e.ExtraFields {
			if v == nil {
				t.Errorf("%s: extra_fields[%q] is nil — the SPA crashes reading .hidden on it",
					e.ConnectionType, name)
			}
		}
	}
}
