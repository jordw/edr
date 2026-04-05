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

const defaultFilesBudget = 2000

// runFiles handles "edr files <pattern>".
// Returns file paths that the trigram index says might contain the pattern.
// Falls back to a full walk + grep if no index exists or pattern < 3 chars.
func runFiles(_ context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("files requires a search pattern argument")
	}
	pattern := args[0]
	edrDir := db.EdrDir()
	budget := flagInt(flags, "budget", 0)
	if budget == 0 && !flagBool(flags, "full", false) {
		budget = defaultFilesBudget
	}

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
			return filesResult(pattern, verified, "index", budget), nil
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

	return filesResult(pattern, matches, "scan", budget), nil
}

func filesResult(pattern string, matches []string, source string, budget int) map[string]any {
	shown, budgetUsed, truncated := truncateFiles(matches, budget)
	result := map[string]any{
		"pattern": pattern,
		"files":   shown,
		"n":       len(matches),
		"source":  source,
	}
	if truncated {
		result["truncated"] = true
		result["budget_used"] = budgetUsed
	}
	return result
}

func truncateFiles(matches []string, budget int) ([]string, int, bool) {
	if budget <= 0 || len(matches) == 0 {
		return matches, 0, false
	}

	shown := make([]string, 0, len(matches))
	used := 0
	for _, match := range matches {
		estimate := estimateFileTokens(match)
		if len(shown) > 0 && used+estimate > budget {
			return shown, used, true
		}
		shown = append(shown, match)
		used += estimate
		if used >= budget {
			if len(shown) < len(matches) {
				return shown, used, true
			}
			break
		}
	}
	return shown, used, len(shown) < len(matches)
}

func estimateFileTokens(path string) int {
	estimate := (len(path) + 3) / 4
	if estimate < 1 {
		return 1
	}
	return estimate + 1
}
