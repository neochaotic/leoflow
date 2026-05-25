// Package leoflow embeds the Python parser and runtime package sources so a
// binary-only install (no source checkout) can provision them. The dev source
// trees under parser/ and runtime/python/ remain the canonical copies; this
// embed reads them directly at build time, so there is no duplicated source.
//
// The all: prefix is required on the package directories: Python packages
// contain __init__.py, and go:embed's default rules drop names beginning with
// "_" or ".". The patterns are scoped to the package dir plus pyproject.toml /
// README.md (what pip needs) so the dev-only test fixtures and dot-caches
// (.pytest_cache, .ruff_cache, .coverage) are not embedded. Build from a clean
// tree (no __pycache__) so stale bytecode is not embedded.
package leoflow

import "embed"

//go:embed all:parser/leoflow_parser parser/pyproject.toml parser/README.md
//go:embed all:runtime/python/leoflow_runtime runtime/python/pyproject.toml runtime/python/README.md
var pythonSources embed.FS

// PythonSources returns the embedded parser and runtime package sources, rooted
// so that "parser/..." and "runtime/python/..." resolve.
func PythonSources() embed.FS { return pythonSources }
