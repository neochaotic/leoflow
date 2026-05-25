// Package workspace provides a filesystem confined to a single root directory.
// Every operation resolves the caller-supplied path against the root and refuses
// any path that would escape it, so it is safe to drive from an HTTP API. It
// backs the Leoflow Lite web editor (ADR 0025); it is never the Production path.
package workspace

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ErrUnsafePath is returned (wrapped) when a requested path is absolute or would
// escape the workspace root. Callers can map it to a client error with
// errors.Is.
var ErrUnsafePath = errors.New("unsafe path")

// noiseDirs are directory names skipped when building the tree: version-control
// and tooling caches that only clutter the editor's file list.
var noiseDirs = map[string]bool{
	".git":          true,
	".hg":           true,
	"node_modules":  true,
	"__pycache__":   true,
	".venv":         true,
	".mypy_cache":   true,
	".ruff_cache":   true,
	".pytest_cache": true,
}

// FS is a filesystem confined to a single root directory. The zero value is not
// usable; construct one with New.
type FS struct {
	root string // absolute, cleaned, symlink-resolved
}

// Entry is one node in the workspace tree. Path is relative to the root and
// always slash-separated.
type Entry struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// New returns an FS rooted at dir. dir must be an existing directory; New
// resolves it to an absolute, symlink-free path so later confinement checks are
// exact.
func New(dir string) (*FS, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace root: %w", err)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("workspace root: %w", err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("workspace root %q is not a directory", dir)
	}
	return &FS{root: resolved}, nil
}

// Root returns the absolute, resolved root directory.
func (f *FS) Root() string { return f.root }

// resolve maps a slash-separated relative path to an absolute path inside the
// root, refusing absolute inputs and any path that escapes the root via "..".
func (f *FS) resolve(rel string) (string, error) {
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%w: %q must be relative to the workspace", ErrUnsafePath, rel)
	}
	clean := path.Clean(filepath.ToSlash(rel))
	if clean == "." {
		return "", fmt.Errorf("%w: %q resolves to the workspace root", ErrUnsafePath, rel)
	}
	// A normalized path that climbs out (".." or "../…") is an escape: reject it
	// rather than silently clamping it back inside the root.
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %q escapes the workspace", ErrUnsafePath, rel)
	}
	abs := filepath.Join(f.root, filepath.FromSlash(clean))
	if abs != f.root && !strings.HasPrefix(abs, f.root+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q escapes the workspace", ErrUnsafePath, rel)
	}
	return abs, nil
}

// Read returns the contents of the file at rel.
func (f *FS) Read(rel string) ([]byte, error) {
	abs, err := f.resolve(rel)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs) //nolint:gosec // abs is confined to the root by resolve
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", rel, err)
	}
	return data, nil
}

// Write saves data to the file at rel, creating any missing parent directories.
func (f *FS) Write(rel string, data []byte) error {
	abs, err := f.resolve(rel)
	if err != nil {
		return err
	}
	if mkErr := os.MkdirAll(filepath.Dir(abs), 0o750); mkErr != nil {
		return fmt.Errorf("creating parent of %q: %w", rel, mkErr)
	}
	if wErr := os.WriteFile(abs, data, 0o600); wErr != nil {
		return fmt.Errorf("writing %q: %w", rel, wErr)
	}
	return nil
}

// Create makes an empty file at rel, or a directory when dir is true, including
// any missing parents.
func (f *FS) Create(rel string, dir bool) error {
	abs, err := f.resolve(rel)
	if err != nil {
		return err
	}
	if dir {
		if mkErr := os.MkdirAll(abs, 0o750); mkErr != nil {
			return fmt.Errorf("creating directory %q: %w", rel, mkErr)
		}
		return nil
	}
	if mkErr := os.MkdirAll(filepath.Dir(abs), 0o750); mkErr != nil {
		return fmt.Errorf("creating parent of %q: %w", rel, mkErr)
	}
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600) //nolint:gosec // abs confined by resolve
	if err != nil {
		return fmt.Errorf("creating %q: %w", rel, err)
	}
	return file.Close()
}

// Delete removes the file or directory (recursively) at rel.
func (f *FS) Delete(rel string) error {
	abs, err := f.resolve(rel)
	if err != nil {
		return err
	}
	if rmErr := os.RemoveAll(abs); rmErr != nil {
		return fmt.Errorf("deleting %q: %w", rel, rmErr)
	}
	return nil
}

// Tree walks the workspace and returns every file and directory (except noise
// directories), with paths relative to the root, sorted for a stable display.
func (f *FS) Tree() ([]Entry, error) {
	var entries []Entry
	err := filepath.WalkDir(f.root, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == f.root {
			return nil
		}
		if d.IsDir() && noiseDirs[d.Name()] {
			return filepath.SkipDir
		}
		rel, relErr := filepath.Rel(f.root, p)
		if relErr != nil {
			return relErr
		}
		var size int64
		if info, infoErr := d.Info(); infoErr == nil {
			size = info.Size()
		}
		entries = append(entries, Entry{
			Path:  filepath.ToSlash(rel),
			Name:  d.Name(),
			IsDir: d.IsDir(),
			Size:  size,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking workspace: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}
