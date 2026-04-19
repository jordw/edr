package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/walk"
)

func runBench(ctx context.Context, db index.SymbolStore, root string, _ []string, _ map[string]any) (any, error) {
	edrDir := db.EdrDir()
	results := []map[string]any{}

	// Repo info
	info := map[string]any{"root": root}
	if h, err := idx.ReadHeader(edrDir); err == nil {
		info["indexed_files"] = int(h.NumFiles)
		info["trigrams"] = int(h.NumTrigrams)
		info["index_complete"] = idx.IsComplete(root, edrDir)
	} else {
		info["indexed_files"] = 0
		info["index_complete"] = false
	}

	// Find representative targets for benchmarks
	bigFile, bigDir, symbolName, symbolFile := discoverBenchTargets(ctx, db, root)

	// Run benchmarks
	type benchCase struct {
		name string
		cmd  string
		args []string
		flags map[string]any
	}

	cases := []benchCase{
		{"orient (root)", "orient", nil, map[string]any{"budget": 80}},
		{"orient --grep", "orient", nil, map[string]any{"budget": 20, "grep": symbolName}},
		{"focus --sig", "focus", []string{bigFile}, map[string]any{"signatures": true}},
		{"index --status", "index", nil, map[string]any{"status": true}},
	}
	if bigDir != "" {
		cases = append(cases, benchCase{"orient (dir)", "orient", []string{bigDir}, map[string]any{"budget": 40}})
	}
	if symbolFile != "" && symbolName != "" {
		cases = append(cases, benchCase{
			"focus symbol", "focus", []string{symbolFile + ":" + symbolName},
			map[string]any{"no_expand": true},
		})
		cases = append(cases, benchCase{
			"focus --expand deps", "focus", []string{symbolFile + ":" + symbolName},
			map[string]any{"expand": "deps"},
		})
	}
	if symbolName != "" {
		cases = append(cases, benchCase{
			"files", "files", []string{symbolName}, map[string]any{},
		})
		cases = append(cases, benchCase{
			"search (text)", "search", []string{symbolName},
			map[string]any{"text": true},
		})
	}

	const runs = 3
	for _, bc := range cases {
		var times []time.Duration
		var lastErr string
		for i := 0; i < runs; i++ {
			t0 := time.Now()
			_, err := Dispatch(ctx, db, bc.cmd, bc.args, bc.flags)
			elapsed := time.Since(t0)
			if err != nil {
				lastErr = err.Error()
				break
			}
			times = append(times, elapsed)
		}
		entry := map[string]any{"name": bc.name}
		if lastErr != "" {
			entry["error"] = lastErr
		} else if len(times) > 0 {
			sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
			median := times[len(times)/2]
			entry["median_ms"] = int(median.Milliseconds())
			if median > 500*time.Millisecond {
				entry["status"] = "slow"
			} else if median > 100*time.Millisecond {
				entry["status"] = "ok"
			} else {
				entry["status"] = "fast"
			}
		}
		results = append(results, entry)
	}

	// Convert to []any for consistent envelope handling
	benchAny := make([]any, len(results))
	for i, r := range results {
		benchAny[i] = r
	}
	info["benchmarks"] = benchAny
	info["targets"] = map[string]any{
		"file":   bigFile,
		"dir":    bigDir,
		"symbol": symbolName,
	}
	return info, nil
}

// discoverBenchTargets finds representative files and symbols for benchmarking.
func discoverBenchTargets(ctx context.Context, db index.SymbolStore, root string) (bigFile, bigDir, symbolName, symbolFile string) {
	// Find a large parseable file that has symbols (functions).
	// Track top 5 largest files, then pick the first with functions.
	type fileInfo struct {
		rel  string
		size int64
	}
	var candidates []fileInfo
	dirSeen := map[string]bool{}

	walk.RepoFiles(root, func(path string) error {
		if !index.Supported(path) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "" {
			return nil
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil
		}
		candidates = append(candidates, fileInfo{rel, info.Size()})
		// Track first-level directories with code
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		if len(parts) > 1 && !dirSeen[parts[0]] {
			dirSeen[parts[0]] = true
			if bigDir == "" {
				bigDir = parts[0]
			}
		}
		return nil
	})

	// Sort by size descending, pick first with functions
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].size > candidates[j].size })
	for _, c := range candidates {
		if len(c.rel) == 0 {
			continue
		}
		bigFile = c.rel
		syms, _ := db.GetSymbolsByFile(ctx, filepath.Join(root, c.rel))
		for _, s := range syms {
			if s.Type == "function" || s.Type == "method" {
				symbolName = s.Name
				symbolFile = c.rel
				return
			}
		}
		// File has no functions — try next
		if symbolName != "" {
			break
		}
	}
	return
}
