package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

// FileResult represents a matched file from find-files.
type FileResult struct {
	File    string `json:"file"`
	Size    int    `json:"size"`     // bytes
	ModTime string `json:"mod_time"` // RFC3339
}

// FindFiles walks the repo and returns files matching a glob pattern.
// Supports ** for recursive matching. Optional dir scopes the search.
func FindFiles(ctx context.Context, root string, pattern string, dir string, budget int) ([]FileResult, error) {
	searchRoot := root
	if dir != "" {
		searchRoot = filepath.Join(root, dir)
		if _, err := os.Stat(searchRoot); err != nil {
			return nil, err
		}
	}

	// Check if pattern uses ** (recursive glob)
	hasDoublestar := strings.Contains(pattern, "**")

	var results []FileResult
	totalSize := 0

	err := index.WalkRepoFiles(searchRoot, func(file string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		rel, err := filepath.Rel(root, file)
		if err != nil {
			return nil
		}

		var matched bool
		if hasDoublestar {
			matched = matchDoublestar(rel, pattern)
		} else {
			// Match against basename only for simple patterns
			matched, _ = filepath.Match(pattern, filepath.Base(file))
		}

		if !matched {
			return nil
		}

		info, err := os.Stat(file)
		if err != nil {
			return nil
		}

		size := int(info.Size())
		if budget > 0 && totalSize > 0 {
			tokenEst := totalSize / 4
			if tokenEst >= budget {
				return nil
			}
		}
		totalSize += size

		results = append(results, FileResult{
			File:    output.Rel(file),
			Size:    size,
			ModTime: info.ModTime().Format(time.RFC3339),
		})

		return nil
	})

	return results, err
}

// matchDoublestar matches a path against a pattern with ** support.
// ** matches any number of path segments.
func matchDoublestar(path, pattern string) bool {
	parts := strings.SplitN(pattern, "**", 2)
	if len(parts) == 1 {
		ok, _ := filepath.Match(pattern, path)
		return ok
	}

	prefix := parts[0] // e.g. "src/" or ""
	suffix := parts[1] // e.g. "/*.go" or ""

	// Check prefix
	if prefix != "" {
		prefix = strings.TrimSuffix(prefix, "/")
		if !strings.HasPrefix(path, prefix+"/") && path != prefix {
			return false
		}
	}

	// Check suffix — match against every possible tail of the path
	suffix = strings.TrimPrefix(suffix, "/")
	if suffix == "" {
		return true
	}

	// Try matching suffix against the basename first (most common: **/*.go)
	if ok, _ := filepath.Match(suffix, filepath.Base(path)); ok {
		return true
	}

	// Try matching against progressively longer tails (e.g. **/test/*.go)
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			tail := path[i+1:]
			if ok, _ := filepath.Match(suffix, tail); ok {
				return true
			}
		}
	}

	return false
}
