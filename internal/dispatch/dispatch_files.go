package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
)

// runFiles handles "edr files <pattern>".
// Returns file paths that the trigram index says might contain the pattern.
// Falls back to a full walk + grep if no index exists or pattern < 3 chars.
func runFiles(_ context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("files requires a search pattern argument")
	}
	pattern := args[0]
	edrDir := db.EdrDir()

	// Detect case sensitivity: mixed case → case-sensitive
	caseSensitive := false
	for _, r := range pattern {
		if r >= 'A' && r <= 'Z' {
			caseSensitive = true
			break
		}
	}

	searchBytes := []byte(pattern)
	searchLower := []byte(strings.ToLower(pattern))

	// Try trigram index (case-sensitive only — index stores raw bytes)
	// Trigrams narrow candidates; we still verify with bytes.Contains.
	if caseSensitive {
		tris := idx.QueryTrigrams(pattern)
		if candidates, ok := idx.Query(edrDir, tris); ok {
			var verified []string
			for _, rel := range candidates {
				data, err := os.ReadFile(filepath.Join(root, rel))
				if err != nil {
					continue
				}
				if bytes.Contains(data, searchBytes) {
					verified = append(verified, rel)
				}
			}
			return map[string]any{
				"pattern": pattern,
				"files":   verified,
				"n":       len(verified),
				"source":  "index",
			}, nil
		}
	}

	// Fallback: walk repo and grep each file
	var matches []string
	index.WalkRepoFiles(root, func(path string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var found bool
		if caseSensitive {
			found = bytes.Contains(data, searchBytes)
		} else {
			found = bytes.Contains(bytes.ToLower(data), searchLower)
		}
		if found {
			if rel, err := filepath.Rel(root, path); err == nil {
				matches = append(matches, rel)
			} else {
				matches = append(matches, path)
			}
		}
		return nil
	})

	return map[string]any{
		"pattern": pattern,
		"files":   matches,
		"n":       len(matches),
		"source":  "scan",
	}, nil
}
