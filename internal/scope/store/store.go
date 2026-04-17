// Package store persists and loads scope.Result data for an entire
// repo. MVP: single-file blob at .edr/scope.bin containing gob-encoded
// per-file results plus mtimes for staleness detection. Full rebuild
// only — incremental dirty-file re-parse is a follow-up.
//
// Consumers (refs-to, rename, etc.) call Load to get the cached Index
// and ResultFor(relPath) to retrieve a parsed file. On a cache miss or
// stale entry, they fall back to on-demand parsing.
package store

import (
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/golang"
	"github.com/jordw/edr/internal/scope/python"
	"github.com/jordw/edr/internal/scope/ts"
)

const (
	indexFile      = "scope.bin"
	currentVersion = 1
)

// Index is the in-memory view of the persisted scope data.
type Index struct {
	Version uint32
	// Results maps repo-relative file path → extracted Result. Key
	// must use forward slashes for cross-platform portability.
	Results map[string]*scope.Result
	// Mtimes captures each file's modification time (unix nanos) at
	// index-build time. Load-time staleness check compares against the
	// current filesystem.
	Mtimes map[string]int64
}

// Parse dispatches to the language-specific scope builder based on
// file extension. Returns nil for unsupported extensions.
func Parse(relPath string, src []byte) *scope.Result {
	ext := strings.ToLower(filepath.Ext(relPath))
	switch ext {
	case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts":
		return ts.Parse(relPath, src)
	case ".go":
		return golang.Parse(relPath, src)
	case ".py", ".pyi":
		return python.Parse(relPath, src)
	}
	return nil
}

// Build walks the repo via walkFn, parses every supported file, and
// writes a fresh scope index to edrDir/scope.bin. Returns the number
// of files indexed.
func Build(root, edrDir string, walkFn func(string, func(string) error) error) (int, error) {
	if err := os.MkdirAll(edrDir, 0755); err != nil {
		return 0, err
	}
	idx := &Index{
		Version: currentVersion,
		Results: map[string]*scope.Result{},
		Mtimes:  map[string]int64{},
	}
	walkErr := walkFn(root, func(absPath string) error {
		ext := strings.ToLower(filepath.Ext(absPath))
		switch ext {
		case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts",
			".go", ".py", ".pyi":
		default:
			return nil
		}
		rel, err := filepath.Rel(root, absPath)
		if err != nil || rel == "" {
			rel = absPath
		}
		// Normalize to forward slashes for key portability.
		rel = filepath.ToSlash(rel)
		src, err := os.ReadFile(absPath)
		if err != nil {
			return nil
		}
		info, err := os.Stat(absPath)
		if err != nil {
			return nil
		}
		result := Parse(rel, src)
		if result == nil {
			return nil
		}
		idx.Results[rel] = result
		idx.Mtimes[rel] = info.ModTime().UnixNano()
		return nil
	})
	if walkErr != nil {
		return 0, walkErr
	}
	if err := idx.save(edrDir); err != nil {
		return 0, err
	}
	return len(idx.Results), nil
}

// save writes the index to edrDir/scope.bin atomically via temp + rename.
func (idx *Index) save(edrDir string) error {
	tmp, err := os.CreateTemp(edrDir, ".scope-*.bin")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	enc := gob.NewEncoder(tmp)
	if err := enc.Encode(idx); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	target := filepath.Join(edrDir, indexFile)
	return os.Rename(tmpName, target)
}

// Load reads the scope index from edrDir. Returns nil if the file
// doesn't exist (first-time query, pre-persistence).
func Load(edrDir string) (*Index, error) {
	f, err := os.Open(filepath.Join(edrDir, indexFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var idx Index
	dec := gob.NewDecoder(f)
	if err := dec.Decode(&idx); err != nil {
		return nil, err
	}
	if idx.Version != currentVersion {
		return nil, fmt.Errorf("scope index version mismatch: got %d, want %d",
			idx.Version, currentVersion)
	}
	return &idx, nil
}

// Exists reports whether a persisted index is present.
func Exists(edrDir string) bool {
	_, err := os.Stat(filepath.Join(edrDir, indexFile))
	return err == nil
}

// ResultFor returns the cached Result for a repo-relative path, or nil
// if not present or the cached entry is stale compared to disk.
func (idx *Index) ResultFor(root, relPath string) *scope.Result {
	if idx == nil {
		return nil
	}
	rel := filepath.ToSlash(relPath)
	result, ok := idx.Results[rel]
	if !ok {
		return nil
	}
	// Staleness: if the file's mtime has changed since the index was
	// built, the cached result is stale. Caller should parse on demand.
	absPath := filepath.Join(root, relPath)
	info, err := os.Stat(absPath)
	if err != nil {
		return nil // file gone
	}
	if idx.Mtimes[rel] != info.ModTime().UnixNano() {
		return nil // stale
	}
	return result
}

// AllResults returns a copy of the file->Result map. Staleness is NOT
// checked — callers that care should walk Mtimes themselves.
func (idx *Index) AllResults() map[string]*scope.Result {
	if idx == nil {
		return nil
	}
	out := make(map[string]*scope.Result, len(idx.Results))
	for k, v := range idx.Results {
		out[k] = v
	}
	return out
}
