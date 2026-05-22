package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// CanonicalHash returns the SHA-256 of the spec's canonical JSON encoding. Go's
// struct marshaling is deterministic (fixed field order, sorted map keys), so
// identical specs hash identically — used to deduplicate DAG versions.
func (d *DAGSpec) CanonicalHash() (string, error) {
	data, err := json.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("encoding spec for hashing: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
