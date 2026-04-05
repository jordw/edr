package index

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/jordw/edr/internal/idx"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// alwaysIgnore contains directories that are always skipped regardless of .gitignore.
var alwaysIgnore = []string{
	".git", ".claude",
}

// DefaultIgnore is the fallback ignore list used when no .gitignore exists.
var DefaultIgnore = []string{
	".git", ".claude", "node_modules", "vendor", "__pycache__",
	".venv", "venv", "target", "build", "dist", ".next",
	".idea", ".vscode", "bin", "obj",
}

func WalkRepoFiles(root string, fn func(path string) error) error {
	gitignore := LoadGitIgnore(root)
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if shouldIgnoreDir(d.Name(), path, root, gitignore) {
				return filepath.SkipDir
			}
			return nil
		}
		if gitignore != nil {
			rel, _ := filepath.Rel(root, path)
			if gitignore.IsIgnored(rel, false) {
				return nil
			}
		}
		info, err := d.Info()
		if err != nil || info.Size() > 1<<20 {
			return nil
		}
		return fn(path)
	})
}

// shouldIgnoreDir returns true if a directory should be skipped.
// Uses .gitignore when available, falls back to DefaultIgnore.
func shouldIgnoreDir(name, path, root string, gitignore *GitIgnoreMatcher) bool {
	// Always skip .git and .claude
	for _, ign := range alwaysIgnore {
		if name == ign {
			return true
		}
	}
	// Skip git submodules (they have a .git file, not a directory)
	if info, err := os.Stat(filepath.Join(path, ".git")); err == nil && !info.IsDir() {
		return true
	}
	if gitignore != nil {
		rel, _ := filepath.Rel(root, path)
		if rel != "." && gitignore.IsIgnored(rel, true) {
			return true
		}
	} else {
		// No .gitignore — use hardcoded fallback
		for _, ign := range DefaultIgnore {
			if name == ign {
				return true
			}
		}
	}
	return false
}

func fileHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:8]) // first 16 hex chars
}

// repoMapConfig holds filters for RepoMap.
type repoMapConfig struct {
	dir        string // only include files under this directory
	glob       string // only include files matching this glob
	symbolType string // filter to this symbol type
	grep       string // only include symbols whose name contains this
	lang       string // filter to files of this language (e.g. "go", "python")
	search     string // filter to symbols whose body contains this text
	hideLocals bool   // hide symbols nested inside functions/methods
	budget     int    // approximate token budget (0 = unlimited)
}

// RepoMapOption configures RepoMap behavior.
type RepoMapOption func(*repoMapConfig)

// WithDir filters repo-map to files under the given directory.
func WithDir(dir string) RepoMapOption {
	return func(c *repoMapConfig) { c.dir = strings.TrimRight(dir, "/") }
}

// WithGlob filters repo-map to files matching the given glob pattern.
func WithGlob(glob string) RepoMapOption {
	return func(c *repoMapConfig) { c.glob = glob }
}

// WithSymbolType filters repo-map to symbols of the given type.
// normalizeSymbolType maps common abbreviations to canonical type names.
var typeAliases = map[string]string{
	"func": "function", "fn": "function",
	"iface": "interface", "intf": "interface",
	"var": "variable",
	"class": "class", "struct": "struct", "method": "method",
	"const": "constant",
}

func WithSymbolType(t string) RepoMapOption {
	return func(c *repoMapConfig) {
		if canonical, ok := typeAliases[t]; ok {
			c.symbolType = canonical
		} else {
			c.symbolType = t
		}
	}
}

// WithGrep filters repo-map to symbols whose name contains the given string.
func WithGrep(grep string) RepoMapOption {
	return func(c *repoMapConfig) { c.grep = grep }
}

// WithHideLocals filters out symbols that are nested inside functions/methods.
func WithHideLocals() RepoMapOption {
	return func(c *repoMapConfig) { c.hideLocals = true }
}

// WithLang filters repo-map to files of the given language.
func WithLang(lang string) RepoMapOption {
	return func(c *repoMapConfig) { c.lang = strings.ToLower(lang) }
}

// WithSearch filters repo-map to symbols whose body contains the given text.
func WithSearch(pattern string) RepoMapOption {
	return func(c *repoMapConfig) { c.search = pattern }
}

// WithBudget sets an approximate token budget for map output.
// RepoMap stops adding files once the budget is exceeded.
func WithBudget(budget int) RepoMapOption {
	return func(c *repoMapConfig) { c.budget = budget }
}

// matchDoublestarPath matches a relative path against a ** glob pattern.
func matchDoublestarPath(path, pattern string) bool {
	parts := strings.SplitN(pattern, "**", 2)
	if len(parts) == 1 {
		ok, _ := filepath.Match(pattern, path)
		return ok
	}
	prefix := strings.TrimSuffix(parts[0], "/")
	suffix := strings.TrimPrefix(parts[1], "/")
	if prefix != "" && !strings.HasPrefix(path, prefix+"/") && path != prefix {
		return false
	}
	if suffix == "" {
		return true
	}
	if ok, _ := filepath.Match(suffix, filepath.Base(path)); ok {
		return true
	}
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			if ok, _ := filepath.Match(suffix, path[i+1:]); ok {
				return true
			}
		}
	}
	return false
}

// RepoMap generates a concise map of the repository structure.
// When dir and/or symbolType filters are set, the query is pushed to SQL
// so only matching rows leave the database. Glob and regex grep are applied
// in Go after the SQL result set.
// RepoMapStats holds truncation metadata for map output.
type RepoMapStats struct {
	Truncated    bool
	ShownFiles   int
	TotalFiles   int
	ShownSymbols int
	TotalSymbols int
	BudgetUsed   int
	// Files contains the structured symbol data, keyed by relative path.
	// Populated when the map is generated; consumers can use this for structured output.
	Files []MapFileEntry
	// DirSummary is populated when truncation is severe (shown < 20% of total).
	// Contains top-level directory counts to help agents pick --dir.
	DirSummary []DirSummaryEntry
}

// DirSummaryEntry summarizes a top-level directory for map orientation.
type DirSummaryEntry struct {
	Dir     string `json:"dir"`
	Files   int    `json:"files"`
	Symbols int    `json:"symbols"`
}

// MapFileEntry is a file with its symbols for structured map output.
type MapFileEntry struct {
	File    string           `json:"file"`
	Symbols []MapSymbolEntry `json:"symbols"`
}

// MapSymbolEntry is a symbol in structured map output.
type MapSymbolEntry struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line,omitempty"`
	Matches int    `json:"matches,omitempty"`
}

func RepoMap(ctx context.Context, db SymbolStore, opts ...RepoMapOption) (string, RepoMapStats, error) {
	cfg := repoMapConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	root := db.Root()

	// Push dir and type filters into SQL; leave glob and regex grep for Go.
	sqlDir := ""
	if cfg.dir != "" {
		sqlDir = filepath.Join(root, cfg.dir)
	}
	sqlName := ""
	// Only push simple substring grep to SQL; regex patterns stay in Go.
	var grepRe *regexp.Regexp
	if cfg.grep != "" {
		var err error
		grepRe, err = regexp.Compile("(?i)(?:" + cfg.grep + ")")
		if err != nil {
			// Not a regex — push as SQL LIKE substring
			sqlName = strings.ToLower(cfg.grep)
		}
		// If it IS valid regex, we could still push a simplified substring
		// to SQL as a pre-filter, but for now keep it simple: regex = Go-side.
	}

	symbols, err := db.FilteredSymbols(ctx, sqlDir, cfg.symbolType, sqlName)
	if err != nil {
		return "", RepoMapStats{}, err
	}

	// Group by file, applying Go-side filters (glob, regex grep)
	byFile := make(map[string][]SymbolInfo)
	var fileOrder []string
	for _, s := range symbols {
		if !IsWithinRoot(root, s.File) {
			continue
		}
		rel, _ := filepath.Rel(root, s.File)
		if rel == "" {
			rel = s.File
		}

		// Glob filter (Go-side: needs path pattern matching)
		if cfg.glob != "" {
			matched := false
			if strings.Contains(cfg.glob, "**") {
				matched = matchDoublestarPath(rel, cfg.glob)
			} else if strings.Contains(cfg.glob, "/") {
				matched, _ = filepath.Match(cfg.glob, rel)
			} else {
				matched, _ = filepath.Match(cfg.glob, filepath.Base(rel))
			}
			if !matched {
				continue
			}
		}

		// Language filter
		if cfg.lang != "" {
			if !matchesLang(rel, cfg.lang) {
				continue
			}
		}

		// Regex grep filter (Go-side when pattern is valid regex)
		if grepRe != nil {
			if !grepRe.MatchString(s.Name) {
				continue
			}
		}

		if _, seen := byFile[s.File]; !seen {
			fileOrder = append(fileOrder, s.File)
		}
		byFile[s.File] = append(byFile[s.File], s)
	}

	// Filter out locals: symbols nested inside functions, methods,
	// or multi-line variable/const blocks (e.g., Cobra command lambdas).
	// Classes, structs, interfaces, modules, and enums are NOT containers
	// for this purpose — their members (methods, fields) are public API.
	if cfg.hideLocals {
		for file, syms := range byFile {
			type span struct{ start, end uint32 }
			var containerSpans []span
			for _, s := range syms {
				if s.StartLine >= s.EndLine {
					continue
				}
				switch s.Type {
				case "function", "method", "variable":
					containerSpans = append(containerSpans, span{s.StartLine, s.EndLine})
				}
			}
			filtered := syms[:0]
			for _, s := range syms {
				isLocal := false
				for _, cs := range containerSpans {
					if s.StartLine > cs.start && s.EndLine <= cs.end {
						isLocal = true
						break
					}
				}
				if !isLocal {
					filtered = append(filtered, s)
				}
			}
			if len(filtered) == 0 {
				delete(byFile, file)
			} else {
				byFile[file] = filtered
			}
		}
	}

	// Body-search filter: keep only symbols whose source lines contain the search text.
	// Reads and filters files in parallel for speed.
	var searchCounts map[string]int // "file\x00symbol" → match count (only when --search)
	if cfg.search != "" {
		searchLower := strings.ToLower(cfg.search)
		searchBytes := []byte(cfg.search)
		searchLowerBytes := []byte(searchLower)
		caseSensitive := false
		for _, r := range cfg.search {
			if r >= 'A' && r <= 'Z' {
				caseSensitive = true
				break
			}
		}

		// Trigram pre-filter: skip indexed files the index says can't match.
		// Unindexed files are left in byFile for the full body-search below.
		if edrDir := HomeEdrDir(root); edrDir != "" {
			queryTris := idx.QueryTrigrams(searchLower)
			indexed := idx.IndexedPaths(edrDir)
			if indexed != nil {
				if candidates, ok := idx.Query(edrDir, queryTris); ok {
					candidateSet := make(map[string]struct{}, len(candidates))
					for _, c := range candidates {
						candidateSet[filepath.Join(root, c)] = struct{}{}
					}
					// Remove indexed files that the trigram index says can't match.
					// Unindexed files stay — they'll be checked by body-search.
					for f := range byFile {
						rel, _ := filepath.Rel(root, f)
						if _, isIndexed := indexed[rel]; !isIndexed {
							continue // not indexed, keep for body-search
						}
						if _, isCandidate := candidateSet[f]; !isCandidate {
							delete(byFile, f)
						}
					}
				}
			}
		}

		type symbolMatch struct {
			sym   SymbolInfo
			count int
		}
		type fileResult struct {
			file    string
			matches []symbolMatch
		}

		fileCh := make(chan string, len(byFile))
		resultCh := make(chan fileResult, len(byFile))
		nWorkers := runtime.NumCPU()
		if nWorkers > len(byFile) {
			nWorkers = len(byFile)
		}
		if nWorkers < 1 {
			nWorkers = 1
		}

		for f := range byFile {
			fileCh <- f
		}
		close(fileCh)

		var wg sync.WaitGroup
		for w := 0; w < nWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for file := range fileCh {
					data, err := CachedReadFile(ctx, file)
					if err != nil {
						continue
					}
					// Whole-file pre-filter: skip files that cannot match.
					if caseSensitive {
						if !bytes.Contains(data, searchBytes) {
							continue
						}
					} else {
						if !bytes.Contains(bytes.ToLower(data), searchLowerBytes) {
							continue
						}
					}
					lines := strings.Split(string(data), "\n")
					syms := byFile[file]
					var matched []symbolMatch
					for _, s := range syms {
						start := int(s.StartLine) - 1
						end := int(s.EndLine)
						if start < 0 {
							start = 0
						}
						if end > len(lines) {
							end = len(lines)
						}
						count := 0
						for _, line := range lines[start:end] {
							if caseSensitive {
								if strings.Contains(line, cfg.search) {
									count++
								}
							} else {
								if strings.Contains(strings.ToLower(line), searchLower) {
									count++
								}
							}
						}
						if count > 0 {
							matched = append(matched, symbolMatch{s, count})
						}
					}
					if len(matched) > 0 {
						resultCh <- fileResult{file, matched}
					}
				}
			}()
		}
		wg.Wait()
		close(resultCh)

		// Rebuild byFile from parallel results and collect match counts.
		searchCounts = map[string]int{} // "file\x00symbol" → count
		for f := range byFile {
			delete(byFile, f)
		}
		for fr := range resultCh {
			var syms []SymbolInfo
			for _, m := range fr.matches {
				syms = append(syms, m.sym)
				searchCounts[fr.file+"\x00"+m.sym.Name] = m.count
			}
			byFile[fr.file] = syms
		}
		// Rebuild fileOrder to exclude files with no matching symbols.
		n := 0
		for _, f := range fileOrder {
			if len(byFile[f]) > 0 {
				fileOrder[n] = f
				n++
			}
		}
		fileOrder = fileOrder[:n]
	}

	// Sort files for budget-friendly output: code first, non-test before test, shallower first, alpha
	sort.SliceStable(fileOrder, func(i, j int) bool {
		ri, _ := filepath.Rel(root, fileOrder[i])
		rj, _ := filepath.Rel(root, fileOrder[j])
		// Code files before non-code (markdown, config, etc.)
		ci := isCodeFile(ri)
		cj := isCodeFile(rj)
		if ci != cj {
			return ci
		}
		ti := isTestOrBenchFile(ri)
		tj := isTestOrBenchFile(rj)
		if ti != tj {
			return !ti
		}
		di := strings.Count(ri, string(filepath.Separator))
		dj := strings.Count(rj, string(filepath.Separator))
		if di != dj {
			return di < dj
		}
		return ri < rj
	})

	// Build output with early-stop budget.
	var b strings.Builder
	budgetChars := cfg.budget * 4
	truncated := false
	filesRendered := 0
	for _, file := range fileOrder {
		syms := byFile[file]
		if len(syms) == 0 {
			continue
		}
		rel, _ := filepath.Rel(root, file)
		if rel == "" {
			rel = file
		}
		fmt.Fprintf(&b, "\n%s\n", rel)
		for _, sym := range syms {
			if searchCounts != nil {
				if c := searchCounts[file+"\x00"+sym.Name]; c > 0 {
					fmt.Fprintf(&b, "  %s %s [%d-%d] (%d matches)\n", sym.Type, sym.Name, sym.StartLine, sym.EndLine, c)
					continue
				}
			}
			fmt.Fprintf(&b, "  %s %s [%d-%d]\n", sym.Type, sym.Name, sym.StartLine, sym.EndLine)
		}
		filesRendered++
		// Early stop: if budget set and exceeded, mark truncated and stop
		if budgetChars > 0 && b.Len() >= budgetChars {
			truncated = true
			break
		}
	}

	// Count totals (filtered, not rendered)
	totalFiles := 0
	totalSymbols := 0
	for _, syms := range byFile {
		if len(syms) > 0 {
			totalFiles++
			totalSymbols += len(syms)
		}
	}

	// Count shown symbols
	shownSymbols := 0
	for _, file := range fileOrder[:filesRendered] {
		shownSymbols += len(byFile[file])
	}

	if !truncated && budgetChars > 0 && filesRendered < totalFiles {
		truncated = true
	}

	// Build structured file entries for the shown files
	var mapFiles []MapFileEntry
	for _, file := range fileOrder[:filesRendered] {
		syms := byFile[file]
		if len(syms) == 0 {
			continue
		}
		rel, _ := filepath.Rel(root, file)
		if rel == "" {
			rel = file
		}
		entry := MapFileEntry{File: rel}
		for _, s := range syms {
			me := MapSymbolEntry{
				Name:    s.Name,
				Kind:    s.Type,
				Line:    int(s.StartLine),
				EndLine: int(s.EndLine),
			}
			if searchCounts != nil {
				me.Matches = searchCounts[file+"\x00"+s.Name]
			}
			entry.Symbols = append(entry.Symbols, me)
		}
		mapFiles = append(mapFiles, entry)
	}

	budgetUsed := 0
	if truncated {
		budgetUsed = b.Len() / 4
	}

	// When truncation is severe (shown < 20% of files), build a directory
	// summary so the agent can orient and pick --dir for the next call.
	var dirSummary []DirSummaryEntry
	if truncated && totalFiles > 0 && filesRendered*5 < totalFiles {
		dirFiles := map[string]map[string]bool{}
		dirSyms := map[string]int{}
		for file, syms := range byFile {
			if len(syms) == 0 {
				continue
			}
			rel, _ := filepath.Rel(root, file)
			if rel == "" {
				continue
			}
			// When --dir is active, show subdirectories within that dir,
			// not top-level repo dirs (which would all collapse to one entry).
			relForDir := rel
			if cfg.dir != "" {
				relForDir = strings.TrimPrefix(rel, cfg.dir+string(filepath.Separator))
			}
			dir := strings.SplitN(relForDir, string(filepath.Separator), 2)[0]
			if !strings.Contains(relForDir, string(filepath.Separator)) {
				dir = "." // files directly in this directory
			}
			if dirFiles[dir] == nil {
				dirFiles[dir] = map[string]bool{}
			}
			dirFiles[dir][file] = true
			dirSyms[dir] += len(syms)
		}
		for dir, files := range dirFiles {
			dirSummary = append(dirSummary, DirSummaryEntry{
				Dir:     dir,
				Files:   len(files),
				Symbols: dirSyms[dir],
			})
		}
		sort.Slice(dirSummary, func(i, j int) bool {
			return dirSummary[i].Symbols > dirSummary[j].Symbols
		})
	}

	return b.String(), RepoMapStats{
		Truncated:    truncated,
		BudgetUsed:   budgetUsed,
		ShownFiles:   filesRendered,
		TotalFiles:   totalFiles,
		ShownSymbols: shownSymbols,
		TotalSymbols: totalSymbols,
		Files:        mapFiles,
		DirSummary:   dirSummary,
	}, nil
}

// langExtensions maps language names to file extensions.
var langExtensions = map[string][]string{
	"go":         {".go"},
	"python":     {".py"},
	"javascript": {".js", ".jsx"},
	"typescript":  {".ts", ".tsx"},
	"rust":       {".rs"},
	"java":       {".java"},
	"c":          {".c", ".h"},
	"cpp":        {".cpp", ".cc", ".hpp", ".hh"},
	"ruby":       {".rb"},
	"php":        {".php"},
	"zig":        {".zig"},
	"lua":        {".lua"},
	"bash":       {".sh", ".bash"},
	"csharp":     {".cs"},
	"kotlin":     {".kt"},
}

// matchesLang returns true if the file matches the given language.
func matchesLang(relPath, lang string) bool {
	exts, ok := langExtensions[lang]
	if !ok {
		// Try as a direct extension match
		ext := strings.ToLower(filepath.Ext(relPath))
		return ext == "."+lang
	}
	ext := strings.ToLower(filepath.Ext(relPath))
	for _, e := range exts {
		if ext == e {
			return true
		}
	}
	return false
}

// isTestOrBenchFile returns true for common test/benchmark file patterns.
// isCodeFile returns true for source code files (not docs, config, data).
func isCodeFile(rel string) bool {
	ext := strings.ToLower(filepath.Ext(rel))
	switch ext {
	case ".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".rs", ".java",
		".c", ".h", ".cpp", ".hpp", ".cc", ".rb", ".php", ".zig",
		".lua", ".sh", ".bash", ".cs", ".kt", ".kts", ".swift":
		return true
	}
	return false
}

func isTestOrBenchFile(rel string) bool {
	base := filepath.Base(rel)
	dir := filepath.Dir(rel)
	if strings.HasPrefix(dir, "bench") || strings.HasPrefix(dir, "test") ||
		strings.HasPrefix(dir, "testdata") || strings.Contains(dir, "/testdata/") {
		return true
	}
	// Go test files
	if strings.HasSuffix(base, "_test.go") {
		return true
	}
	// Common test file patterns: test_*.py, *_test.js, *.test.ts, *.spec.ts
	if strings.HasPrefix(base, "test_") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return true
	}
	return false
}
