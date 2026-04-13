package dispatch

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

const defaultSearchBudget = 2000

// runSearchUnified handles the "search" command dispatched from batch -s.
// Routes to text search (grep-style) or symbol search (orient --grep).
func runSearchUnified(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("search requires a pattern argument")
	}
	pattern := args[0]

	// --in scopes to a symbol body
	if inSpec := flagString(flags, "in", ""); inSpec != "" {
		return runSearchInFile(ctx, db, root, pattern, inSpec, flags)
	}

	// Detect text search mode: any grep-style flag triggers text search.
	isText := flagBool(flags, "text", false) ||
		flagBool(flags, "regex", false) ||
		len(flagStringSlice(flags, "include")) > 0 ||
		len(flagStringSlice(flags, "exclude")) > 0 ||
		flagInt(flags, "context", 0) > 0

	if isText {
		return runTextSearch(ctx, db, root, pattern, flags)
	}

	// When the symbol index is dirty, symbol search reparses all files
	// (very slow on large repos). Skip it and go straight to text search
	// which uses the trigram index and is much faster.
	edrDir := db.EdrDir()
	symIndexDirty := idx.IsDirty(edrDir) || !idx.HasSymbolIndex(edrDir)

	if symIndexDirty {
		return runTextSearch(ctx, db, root, pattern, flags)
	}

	// Default: symbol search via orient --grep, with auto-fallback to text.
	result, err := runSymbolSearch(ctx, db, root, pattern, flags)
	if err != nil {
		// Regex errors mean the pattern isn't a valid symbol grep
		// (e.g. "printk(KERN_INFO"). Fall through to text search
		// which handles literal patterns.
		if strings.Contains(err.Error(), "invalid --grep regex") {
			return runTextSearch(ctx, db, root, pattern, flags)
		}
		return nil, err
	}
	// Auto-fallback: if symbol search returned nothing, retry as text search
	if m, ok := result.(map[string]any); ok {
		if n, _ := m["total_matches"].(int); n == 0 {
			textResult, textErr := runTextSearch(ctx, db, root, pattern, flags)
			if textErr == nil {
				if tm, ok := textResult.(map[string]any); ok {
					if tn, _ := tm["total_matches"].(int); tn > 0 {
						tm["hint"] = fmt.Sprintf("no symbol matches; auto-retried as text search (%d matches)", tn)
						return tm, nil
					}
				}
			}
		}
	}
	return result, nil
}

// runSymbolSearch searches symbols by name using orient --grep.
func runSymbolSearch(ctx context.Context, db index.SymbolStore, root, pattern string, flags map[string]any) (any, error) {
	budget := flagInt(flags, "budget", defaultSearchBudget)
	limit := flagInt(flags, "limit", 100)
	bodyFlag := flagBool(flags, "body", false)

	var opts []index.RepoMapOption
	if bodyFlag {
		opts = append(opts, index.WithSearch(pattern))
	} else {
		opts = append(opts, index.WithGrep(pattern))
	}
	opts = append(opts, index.WithHideLocals())
	// Use a generous budget for the orient query — we cap results by limit.
	opts = append(opts, index.WithBudget(budget*2))

	_, stats, err := index.RepoMap(ctx, db, opts...)
	if err != nil {
		return nil, err
	}

	// Convert orient output to search result format, capped by limit.
	totalMatches := 0
	truncated := false
	var files []map[string]any
	for _, fe := range stats.Files {
		if totalMatches >= limit {
			truncated = true
			break
		}
		var matches []map[string]any
		for _, sym := range fe.Symbols {
			if totalMatches >= limit {
				truncated = true
				break
			}
			matches = append(matches, map[string]any{
				"symbol": map[string]any{
					"file": fe.File,
					"name": sym.Name,
					"type": sym.Kind,
					"line": sym.Line,
				},
			})
			totalMatches++
		}
		if len(matches) > 0 {
			fileResult := map[string]any{
				"file":    fe.File,
				"matches": toAnySlice(matches),
			}
			files = append(files, fileResult)
		}
	}

	// Determine source
	symSource := "parse"
	if edrDir := db.EdrDir(); idx.HasSymbolIndex(edrDir) && !idx.IsDirty(edrDir) {
		symSource = "symbol_index"
	} else if edrDir := db.EdrDir(); idx.IsDirty(edrDir) {
		symSource = "parse (index dirty)"
	}

	result := map[string]any{
		"total_matches": totalMatches,
		"type":          "search",
		"source":        symSource,
	}
	if totalMatches == 0 {
		result["root"] = output.Rel(root)
	}
	if len(files) > 0 {
		result["files"] = toAnySlice(files)
	}
	if truncated || stats.Truncated {
		result["truncated"] = true
	}
	return result, nil
}

// runTextSearch performs grep-style text search across repo files.
func runTextSearch(ctx context.Context, db index.SymbolStore, root, pattern string, flags map[string]any) (any, error) {
	budget := flagInt(flags, "budget", defaultSearchBudget)
	limit := flagInt(flags, "limit", 100)
	contextLines := flagInt(flags, "context", 2) // default 2 lines context
	noGroup := flagBool(flags, "no_group", false)
	includes := flagStringSlice(flags, "include")
	excludes := flagStringSlice(flags, "exclude")

	// Build regex
	isRegex := flagBool(flags, "regex", false)
	var re *regexp.Regexp
	var err error
	if isRegex {
		re, err = regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid regex %q: %w", pattern, err)
		}
	} else {
		re, err = regexp.Compile(regexp.QuoteMeta(pattern))
		if err != nil {
			return nil, fmt.Errorf("internal error compiling pattern: %w", err)
		}
	}

	type match struct {
		File    string
		Line    int
		Text    string
		Snippet string // set when contextLines > 0
	}

	var matches []match
	totalMatches := 0
	budgetUsed := 0

	// includeExcludeFilter returns true if the rel path passes include/exclude filters.
	includeExcludeFilter := func(rel string) bool {
		if len(includes) > 0 {
			matched := false
			for _, inc := range includes {
				if ok, _ := filepath.Match(inc, filepath.Base(rel)); ok {
					matched = true
					break
				}
				if ok, _ := filepath.Match(inc, rel); ok {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
		for _, exc := range excludes {
			if ok, _ := filepath.Match(exc, filepath.Base(rel)); ok {
				return false
			}
			if ok, _ := filepath.Match(exc, rel); ok {
				return false
			}
		}
		return true
	}

	// searchFile reads and searches a single file, appending matches.
	searchFile := func(rel, absPath string) {
		data, err := os.ReadFile(absPath)
		if err != nil {
			return
		}
		// Pre-filter: skip files that don't contain the pattern bytes
		if !isRegex && !bytes.Contains(data, []byte(pattern)) {
			return
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if totalMatches >= limit || (budget > 0 && budgetUsed >= budget) {
				break
			}
			if re.MatchString(line) {
				m := match{File: rel, Line: i + 1, Text: strings.TrimSpace(line)}
				if contextLines > 0 {
					start := i - contextLines
					if start < 0 {
						start = 0
					}
					end := i + contextLines + 1
					if end > len(lines) {
						end = len(lines)
					}
					var snippet strings.Builder
					for j := start; j < end; j++ {
						fmt.Fprintf(&snippet, "%d: %s\n", j+1, lines[j])
					}
					m.Snippet = snippet.String()
					budgetUsed += end - start
				} else {
					budgetUsed++
				}
				matches = append(matches, m)
				totalMatches++
			}
		}
	}

	atLimit := func() bool {
		return totalMatches >= limit || (budget > 0 && budgetUsed >= budget)
	}

	// Trigram pre-filter + stat-check for changed files.
	edrDir := db.EdrDir()
	var candidates map[string]struct{}
	h, _ := idx.ReadHeader(edrDir)
	hasIndex := h != nil

	var changes *idx.Changes
	if hasIndex {
		changes = idx.StatChanges(root, edrDir)
	}

	changedSet := make(map[string]bool)
	if changes != nil {
		for _, f := range changes.Modified {
			changedSet[f] = true
		}
		for _, f := range changes.Deleted {
			changedSet[f] = true
		}
	}

	if !isRegex && hasIndex {
		tris := idx.QueryTrigrams(strings.ToLower(pattern))
		if len(tris) > 0 {
			if paths, ok := idx.Query(edrDir, tris); ok {
				candidates = make(map[string]struct{}, len(paths))
				for _, p := range paths {
					candidates[p] = struct{}{}
				}
				for _, rel := range paths {
					if atLimit() {
						break
					}
					if changedSet[rel] {
						continue // rescanned below
					}
					if !includeExcludeFilter(rel) {
						continue
					}
					searchFile(rel, filepath.Join(root, rel))
				}
			}
		}
	}

	// Scan changed + new files — their trigrams may be stale or absent.
	if changes != nil && !changes.Empty() {
		scanList := append(changes.Modified, changes.New...)
		for _, rel := range scanList {
			if atLimit() {
				break
			}
			if !includeExcludeFilter(rel) {
				continue
			}
			searchFile(rel, filepath.Join(root, rel))
		}
	} else if !hasIndex {
		// No index at all — must walk.
		index.WalkRepoFiles(root, func(path string) error {
			if atLimit() {
				return filepath.SkipAll
			}
			rel, _ := filepath.Rel(root, path)
			if rel == "" {
				rel = path
			}
			if !includeExcludeFilter(rel) {
				return nil
			}
			searchFile(rel, path)
			return nil
		})
	}

	// Determine search source for provenance
	searchSource := "scan"
	if candidates != nil {
		searchSource = "index"
		if changes != nil && !changes.Empty() {
			searchSource = "index+stat"
		}
	}

	// Build result
	result := map[string]any{
		"total_matches": totalMatches,
		"type":          "search",
		"source":        searchSource,
	}
	// Surface repo root on empty results so agents can detect wrong-repo targeting.
	if totalMatches == 0 {
		result["root"] = output.Rel(root)
	}

	if noGroup || len(matches) == 0 {
		flat := make([]map[string]any, len(matches))
		for i, m := range matches {
			flat[i] = matchToMap(m.File, m.Line, m.Text, m.Snippet)
		}
		result["matches"] = toAnySlice(flat)
	} else {
		// Group by file
		grouped := map[string][]match{}
		var order []string
		for _, m := range matches {
			if _, seen := grouped[m.File]; !seen {
				order = append(order, m.File)
			}
			grouped[m.File] = append(grouped[m.File], m)
		}
		var files []map[string]any
		for _, f := range order {
			ms := grouped[f]
			fileMatches := make([]map[string]any, len(ms))
			for i, m := range ms {
				fileMatches[i] = matchToMap("", m.Line, m.Text, m.Snippet)
			}
			files = append(files, map[string]any{
				"file":    f,
				"matches": toAnySlice(fileMatches),
			})
		}
		result["files"] = toAnySlice(files)
	}

	if budget > 0 && budgetUsed >= budget {
		result["truncated"] = true
	}

	return result, nil
}

// runSearchInFile scopes text search to a specific file, optionally within a symbol.
func runSearchInFile(ctx context.Context, db index.SymbolStore, root, pattern, inSpec string, flags map[string]any) (any, error) {
	parts := splitFileSymbol(inSpec)
	file := parts[0]
	sym := ""
	if len(parts) > 1 {
		sym = parts[1]
	}
	resolved, err := index.ResolvePath(root, file)
	if err != nil {
		return nil, fmt.Errorf("--in: %w", err)
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", file, err)
	}

	startLine := 1
	endLine := 0 // 0 = all

	// If symbol specified, find its line range
	if sym != "" {
		rel, _ := filepath.Rel(root, resolved)
		syms, _ := db.GetSymbolsByFile(ctx, rel)
		found := false
		for _, s := range syms {
			if s.Name == sym {
				startLine = int(s.StartLine)
				endLine = int(s.EndLine)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("symbol %q not found in %s", sym, file)
		}
	}

	re, err := regexp.Compile(regexp.QuoteMeta(pattern))
	if err != nil {
		return nil, err
	}

	rel, _ := filepath.Rel(root, resolved)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineNum := 0
	var matches []map[string]any
	for scanner.Scan() {
		lineNum++
		if lineNum < startLine {
			continue
		}
		if endLine > 0 && lineNum > endLine {
			break
		}
		line := scanner.Text()
		if re.MatchString(line) {
			matches = append(matches, matchToMap("", lineNum, strings.TrimSpace(line), ""))
		}
	}

	result := map[string]any{
		"total_matches": len(matches),
		"type":          "search",
		"files": toAnySlice([]map[string]any{{
			"file":    rel,
			"matches": toAnySlice(matches),
		}}),
	}
	return result, nil
}

func matchToMap(file string, line int, text, snippet string) map[string]any {
	m := map[string]any{"line": line, "text": text}
	if file != "" {
		m["file"] = file
	}
	if snippet != "" {
		m["snippet"] = snippet
	}
	return m
}

func toAnySlice[T any](s []T) []any {
	result := make([]any, len(s))
	for i, v := range s {
		result[i] = v
	}
	return result
}