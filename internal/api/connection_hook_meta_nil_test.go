package api

import "testing"

// TestConnectionHookMetaHasNoNilFieldSpecs guards the reported connector-config
// crash: the Airflow 3.2 FlexibleForm reads `field.hidden` for every entry in
// standard_fields/extra_fields, so a nil value (the generated catalog emitted
// "description": null) crashed the whole Add/Edit Connection page with
// "Cannot read properties of undefined (reading 'hidden')". The served catalog
// must never contain a nil field spec — across ALL connectors, not just the
// sampled ones.
func TestConnectionHookMetaHasNoNilFieldSpecs(t *testing.T) {
	cat := connectionTypeCatalog()
	if len(cat) == 0 {
		t.Fatal("connection catalog is empty")
	}
	for _, e := range cat {
		for name, v := range e.StandardFields {
			if v == nil {
				t.Errorf("%s: standard_fields[%q] is nil — the SPA crashes reading .hidden on it",
					e.ConnectionType, name)
			}
		}
		for name, v := range e.ExtraFields {
			if v == nil {
				t.Errorf("%s: extra_fields[%q] is nil — same FlexibleForm crash", e.ConnectionType, name)
			}
		}
	}
}
