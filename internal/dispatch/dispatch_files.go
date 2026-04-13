package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
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

	// Query trigram index + stat-check for changed files.
	var matches []string
	source := "scan"

	tris := idx.QueryTrigrams(strings.ToLower(pattern))
	h, _ := idx.ReadHeader(edrDir)
	hasIndex := h != nil

	// Stat all indexed files to find modifications, deletions, and new files.
	// ~150ms on 93K-file repos — always correct, no stale dirty tracking.
	var changes *idx.Changes
	if hasIndex {
		changes = idx.StatChanges(root, edrDir)
	}

	if hasIndex && len(tris) > 0 {
		if candidates, ok := idx.Query(edrDir, tris); ok {
			// Build set of changed files to skip in trigram results
			// (they get rescanned below with current content).
			changedSet := make(map[string]bool)
			if changes != nil {
				for _, f := range changes.Modified {
					changedSet[f] = true
				}
				for _, f := range changes.Deleted {
					changedSet[f] = true
				}
			}
			for _, rel := range candidates {
				if changedSet[rel] {
					continue // rescanned below
				}
				data, err := os.ReadFile(filepath.Join(root, rel))
				if err != nil {
					continue
				}
				if fileMatches(data, searchBytes, searchLower, caseSensitive) {
					matches = append(matches, rel)
				}
			}
			source = "index"
		}
	}

	// Scan changed + new files — their trigrams may be stale or absent.
	if changes != nil && !changes.Empty() {
		scanList := append(changes.Modified, changes.New...)
		for _, rel := range scanList {
			data, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				continue
			}
			if fileMatches(data, searchBytes, searchLower, caseSensitive) {
				matches = append(matches, rel)
			}
		}
		if source == "index" {
			source = "index+stat"
		}
	} else if !hasIndex {
		// No index at all — must walk.
		index.WalkRepoFiles(root, func(path string) error {
			rel, _ := filepath.Rel(root, path)
			if rel == "" {
				rel = path
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			if fileMatches(data, searchBytes, searchLower, caseSensitive) {
				matches = append(matches, rel)
			}
			return nil
		})
	}

	if !hasIndex {
		source = "scan"
	}
	sort.Strings(matches)
	result := filesResult(pattern, matches, source, budget)
	if len(matches) == 0 {
		result["root"] = output.Rel(root)
	}
	return result, nil
}

func fileMatches(data, searchBytes, searchLower []byte, caseSensitive bool) bool {
	if caseSensitive {
		return bytes.Contains(data, searchBytes)
	}
	return bytes.Contains(bytes.ToLower(data), searchLower)
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
