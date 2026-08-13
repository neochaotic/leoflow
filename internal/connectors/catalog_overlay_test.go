package connectors

import "testing"

func mustConn(t *testing.T, ct string) Connector {
	t.Helper()
	for _, c := range Catalog() {
		if c.ConnectionType == ct {
			return c
		}
	}
	t.Fatalf("connector %q not in catalog", ct)
	return Connector{}
}

// TestDBTAuthOverlayMergesLeoflowFields pins the leoflow field overlay: the
// generated catalog carries only what Airflow's provider introspection emits,
// but leoflow's dbt profile mapping accepts auth fields Airflow does not
// describe — Snowflake's key-pair passphrase, BigQuery's keyless `method`
// selector, and Databricks' OAuth-M2M service-principal fields. Without a
// labeled form field a user must hand-type these into the raw `extra` JSON box
// (#587 follow-up). The overlay adds them; this asserts they surface AND that
// the generated fields are preserved (overlay augments, never replaces).
func TestDBTAuthOverlayMergesLeoflowFields(t *testing.T) {
	// Snowflake: the encrypted-key passphrase, added next to the generated
	// private_key_content / private_key_file fields.
	sf := mustConn(t, "snowflake")
	if _, ok := sf.ExtraFields["private_key_passphrase"]; !ok {
		t.Error("snowflake overlay must add private_key_passphrase")
	}
	for _, base := range []string{"account", "private_key_content", "private_key_file", "warehouse"} {
		if _, ok := sf.ExtraFields[base]; !ok {
			t.Errorf("overlay dropped the generated snowflake field %q", base)
		}
	}

	// BigQuery (google_cloud_platform): the keyless auth selector.
	gcp := mustConn(t, "google_cloud_platform")
	if _, ok := gcp.ExtraFields["method"]; !ok {
		t.Error("google_cloud_platform overlay must add the keyless `method` selector")
	}
	if _, ok := gcp.ExtraFields["keyfile_dict"]; !ok {
		t.Error("overlay dropped the generated google_cloud_platform keyfile_dict field")
	}

	// Databricks: Airflow ships an empty form for it, so the overlay supplies
	// the whole dbt-auth field set — including a labeled host + PAT.
	db := mustConn(t, "databricks")
	for _, k := range []string{"http_path", "client_id", "client_secret", "auth_type"} {
		if _, ok := db.ExtraFields[k]; !ok {
			t.Errorf("databricks overlay must add extra field %q", k)
		}
	}
	if db.StandardFields["host"] == nil {
		t.Error("databricks overlay must surface a labeled host (workspace URL) field")
	}
}

// TestDBTAuthOverlayTargetsRealConnectors guards against a typo'd conn_type in
// the overlay silently doing nothing: every overlay key must name a connector
// that exists in the generated catalog.
func TestDBTAuthOverlayTargetsRealConnectors(t *testing.T) {
	known := map[string]bool{}
	for _, c := range Catalog() {
		known[c.ConnectionType] = true
	}
	for ct := range fieldOverlay {
		if !known[ct] {
			t.Errorf("overlay targets unknown connector %q (typo? provider not installed when catalog was generated?)", ct)
		}
	}
}
