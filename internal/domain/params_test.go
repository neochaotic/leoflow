package domain

import (
	"encoding/json"
	"testing"
)

// TestParamsRoundTripAndValidate pins that a DAGSpec carrying author-declared
// params round-trips through JSON in the {name:{default,schema}} shape and passes
// schema validation, so a compiled dag.json with params registers cleanly.
func TestParamsRoundTripAndValidate(t *testing.T) {
	spec := DAGSpec{
		SchemaVersion: "1.0",
		DagID:         "etl",
		DagVersion:    "v1",
		Image:         "img:v1",
		Params: map[string]ParamSpec{
			"limit": {Default: json.RawMessage(`5`), Schema: json.RawMessage(`{}`)},
			"n":     {Default: json.RawMessage(`3`), Schema: json.RawMessage(`{"type":"integer","minimum":1}`)},
		},
		Tasks: []TaskSpec{{TaskID: "a", Type: TaskTypePython, Entrypoint: "dag:a"}},
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("spec with params should validate: %v", err)
	}
	data, err := json.Marshal(&spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back DAGSpec
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Params) != 2 {
		t.Fatalf("params did not round-trip: %+v", back.Params)
	}
	if string(back.Params["n"].Schema) != `{"type":"integer","minimum":1}` {
		t.Errorf("schema lost on round-trip: %s", back.Params["n"].Schema)
	}
}

// TestParamsPartOfCanonicalHash pins that params are part of the immutable spec:
// changing a param default (or schema) yields a different version hash, so a DAG
// re-registered with new param defaults is a new version.
func TestParamsPartOfCanonicalHash(t *testing.T) {
	base := DAGSpec{
		SchemaVersion: "1.0", DagID: "etl", DagVersion: "v1", Image: "img:v1",
		Tasks: []TaskSpec{{TaskID: "a", Type: TaskTypePython, Entrypoint: "dag:a"}},
	}
	withParam := base
	withParam.Params = map[string]ParamSpec{"limit": {Default: json.RawMessage(`5`), Schema: json.RawMessage(`{}`)}}

	h0, err := base.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	h1, err := withParam.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if h0 == h1 {
		t.Error("declaring params must change the canonical hash")
	}
}

// TestNoParamsOmitsKey pins the back-compatible shape: a spec that declares no
// params marshals without a "params" key at all.
func TestNoParamsOmitsKey(t *testing.T) {
	spec := DAGSpec{
		SchemaVersion: "1.0", DagID: "etl", DagVersion: "v1", Image: "img:v1",
		Tasks: []TaskSpec{{TaskID: "a", Type: TaskTypePython, Entrypoint: "dag:a"}},
	}
	data, err := json.Marshal(&spec)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["params"]; ok {
		t.Errorf("a param-free spec must omit the params key, got %s", data)
	}
}

// paramSpecWith builds an otherwise-valid DAGSpec declaring a single param, so a
// Validate() failure can only come from that param.
func paramSpecWith(name string, p ParamSpec) DAGSpec {
	return DAGSpec{
		SchemaVersion: "1.0", DagID: "etl", DagVersion: "v1", Image: "img:v1",
		Params: map[string]ParamSpec{name: p},
		Tasks:  []TaskSpec{{TaskID: "a", Type: TaskTypePython, Entrypoint: "dag:a"}},
	}
}

// TestValidateRejectsParamDefaultViolatingSchema pins that a param whose default
// breaks its own schema is refused at registration, not left to 400 on every
// trigger.
func TestValidateRejectsParamDefaultViolatingSchema(t *testing.T) {
	spec := paramSpecWith("n", ParamSpec{
		Default: json.RawMessage(`0`),
		Schema:  json.RawMessage(`{"type":"integer","minimum":1}`),
	})
	if err := spec.Validate(); err == nil {
		t.Error("want error: default 0 violates minimum 1")
	}
}

// TestValidateRejectsUncompilableParamSchema pins that a param whose schema does
// not compile is refused at registration.
func TestValidateRejectsUncompilableParamSchema(t *testing.T) {
	spec := paramSpecWith("n", ParamSpec{
		Default: json.RawMessage(`1`),
		Schema:  json.RawMessage(`{"type":123}`),
	})
	if err := spec.Validate(); err == nil {
		t.Error("want error: {\"type\":123} is not a valid JSON Schema")
	}
}

// TestValidateAllowsNullDefaultWithSchema pins that a required-style param (no
// default / null default) with a schema still registers — the null default is
// not checked against the schema.
func TestValidateAllowsNullDefaultWithSchema(t *testing.T) {
	spec := paramSpecWith("n", ParamSpec{
		Default: json.RawMessage(`null`),
		Schema:  json.RawMessage(`{"type":"integer","minimum":1}`),
	})
	if err := spec.Validate(); err != nil {
		t.Errorf("a null default with a schema should register: %v", err)
	}
}
