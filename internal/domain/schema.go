package domain

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schemas/dag-schema.json
var dagSchemaJSON []byte

//go:embed schemas/leoflow-yaml-schema.json
var leoflowSchemaJSON []byte

// compiledSchemas holds the parsed JSON Schemas used to validate domain types.
type compiledSchemas struct {
	dag     *jsonschema.Schema
	leoflow *jsonschema.Schema
}

// schemas compiles the embedded schemas exactly once, on first use.
var schemas = sync.OnceValues(loadSchemas)

func loadSchemas() (compiledSchemas, error) {
	dag, err := compileSchema("dag.json", dagSchemaJSON)
	if err != nil {
		return compiledSchemas{}, fmt.Errorf("compiling dag schema: %w", err)
	}
	leoflow, err := compileSchema("leoflow.yaml", leoflowSchemaJSON)
	if err != nil {
		return compiledSchemas{}, fmt.Errorf("compiling leoflow schema: %w", err)
	}
	return compiledSchemas{dag: dag, leoflow: leoflow}, nil
}

func compileSchema(name string, raw []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(name, doc); err != nil {
		return nil, err
	}
	return c.Compile(name)
}

// validateAgainst marshals v to JSON and validates it against the schema,
// returning the aggregated schema violations (or nil when v conforms).
func validateAgainst(sch *jsonschema.Schema, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshaling for validation: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("decoding for validation: %w", err)
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("schema validation: %w", err)
	}
	return nil
}
