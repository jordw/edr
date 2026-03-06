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
