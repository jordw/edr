// generate.go extracts ambiguous symbol queries from a repo and dumps
// candidate lists as JSONL for labeling.
//
// Usage:
//
//	go run scripts/ranking/generate.go /path/to/repo [--limit 500] [--min-candidates 3] [--max-candidates 50]
//
// Output: JSONL to stdout, one ExportExample per line.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/ranking"
)

func main() {
	limit := flag.Int("limit", 500, "max queries to generate")
	minCand := flag.Int("min-candidates", 3, "minimum candidates for a query to be interesting")
	maxCand := flag.Int("max-candidates", 50, "maximum candidates per query")
	seed := flag.Int64("seed", 0, "random seed (0 = use repo name hash)")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: generate [flags] /path/to/repo")
		os.Exit(1)
	}

	repoRoot := flag.Arg(0)
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad path: %v\n", err)
		os.Exit(1)
	}

	// Normalize
	if normalized, err := index.NormalizeRoot(absRoot); err == nil {
		absRoot = normalized
	}

	fmt.Fprintf(os.Stderr, "Opening repo: %s\n", absRoot)

	db := index.NewOnDemand(absRoot)
	defer db.Close()

	ctx := context.Background()

	// Get all symbols — prefer AllSymbols (uses symbol index if available)
	fmt.Fprintln(os.Stderr, "Loading symbols...")
	allSymbols, err := db.AllSymbols(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load symbols: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Found %d symbols\n", len(allSymbols))

	// Group by lowercase name
	byName := map[string][]index.SymbolInfo{}
	for _, s := range allSymbols {
		key := strings.ToLower(s.Name)
		byName[key] = append(byName[key], s)
	}

	// Filter to ambiguous names (multiple candidates)
	type nameEntry struct {
		name  string
		count int
	}
	var ambiguous []nameEntry
	for name, syms := range byName {
		if len(syms) >= *minCand {
			ambiguous = append(ambiguous, nameEntry{name, len(syms)})
		}
	}
	fmt.Fprintf(os.Stderr, "Found %d ambiguous names (>=%d candidates)\n", len(ambiguous), *minCand)

	if len(ambiguous) == 0 {
		fmt.Fprintln(os.Stderr, "No ambiguous names found")
		os.Exit(0)
	}

	// Sort by candidate count descending for priority, then shuffle within tiers
	sort.Slice(ambiguous, func(i, j int) bool {
		return ambiguous[i].count > ambiguous[j].count
	})

	// Seed RNG
	rngSeed := *seed
	if rngSeed == 0 {
		// Deterministic seed from repo name
		h := int64(0)
		for _, c := range filepath.Base(absRoot) {
			h = h*31 + int64(c)
		}
		rngSeed = h
	}
	rng := rand.New(rand.NewSource(rngSeed))

	// Sample queries: mix of high-ambiguity and random
	var queries []string

	// Take top 20% most ambiguous
	topN := len(ambiguous) / 5
	if topN > *limit/2 {
		topN = *limit / 2
	}
	for i := 0; i < topN && i < len(ambiguous); i++ {
		queries = append(queries, ambiguous[i].name)
	}

	// Shuffle the rest and sample
	rest := ambiguous[topN:]
	rng.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
	for i := 0; i < len(rest) && len(queries) < *limit; i++ {
		queries = append(queries, rest[i].name)
	}

	// Shuffle final order
	rng.Shuffle(len(queries), func(i, j int) { queries[i], queries[j] = queries[j], queries[i] })

	fmt.Fprintf(os.Stderr, "Generating %d queries\n", len(queries))

	// Generate candidate lists
	repo := filepath.Base(absRoot)
	enc := json.NewEncoder(os.Stdout)
	generated := 0

	for _, query := range queries {
		syms := byName[query]
		if len(syms) < *minCand {
			continue
		}

		// Deduplicate by file:name
		seen := map[string]bool{}
		var deduped []index.SymbolInfo
		for _, s := range syms {
			rel, _ := filepath.Rel(absRoot, s.File)
			key := rel + ":" + s.Name
			if !seen[key] {
				seen[key] = true
				deduped = append(deduped, s)
			}
		}

		// Cap candidates
		if len(deduped) > *maxCand {
			// Keep a diverse sample: sort by file path, take every Nth
			sort.Slice(deduped, func(i, j int) bool { return deduped[i].File < deduped[j].File })
			step := len(deduped) / *maxCand
			if step < 1 {
				step = 1
			}
			var sampled []index.SymbolInfo
			for i := 0; i < len(deduped) && len(sampled) < *maxCand; i += step {
				sampled = append(sampled, deduped[i])
			}
			deduped = sampled
		}

		// Count symbols per file for the FileSymbolCount feature
		fileSymCount := map[string]int{}
		for _, s := range deduped {
			fileSymCount[s.File]++
		}

		// Build candidate features
		candidates := make([]ranking.CandidateFeatures, len(deduped))
		for i, s := range deduped {
			rel, _ := filepath.Rel(absRoot, s.File)
			candidates[i] = ranking.CandidateFeatures{
				Name:            s.Name,
				Type:            s.Type,
				File:            rel,
				StartLine:       s.StartLine,
				EndLine:         s.EndLine,
				FileSymbolCount: fileSymCount[s.File],
			}
		}

		ex := ranking.ExportCandidateList(query, absRoot, candidates)
		ex.Repo = repo
		// Attach source snippets (first 3 lines of each candidate body)
		for i, s := range deduped {
			snippet := readSnippet(s.File, int(s.StartLine), 3)
			if snippet != "" {
				ex.Candidates[i].Snippet = snippet
			}
		}
		enc.Encode(ex)
		generated++
	}

	fmt.Fprintf(os.Stderr, "Generated %d candidate lists\n", generated)
}

// readSnippet reads n lines starting at startLine (1-based) from a file.
func readSnippet(path string, startLine, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.SplitAfter(string(data), "\n")
	start := startLine - 1
	if start < 0 {
		start = 0
	}
	end := start + n
	if end > len(lines) {
		end = len(lines)
	}
	if start >= end {
		return ""
	}
	var b strings.Builder
	for _, line := range lines[start:end] {
		b.WriteString(line)
	}
	s := strings.TrimRight(b.String(), "\n")
	// Cap at 200 chars to keep JSONL reasonable
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
