package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// alwaysIgnore contains directories that are always skipped regardless of .gitignore.
var alwaysIgnore = []string{
	".git", ".edr", ".claude",
}

// DefaultIgnore is the fallback ignore list used when no .gitignore exists.
var DefaultIgnore = []string{
	".git", ".edr", ".claude", "node_modules", "vendor", "__pycache__",
	".venv", "venv", "target", "build", "dist", ".next",
	".idea", ".vscode", "bin", "obj",
}

// HasStaleFiles checks whether the repo has changed since the last index.
// Uses a two-tier approach: first compares mtime metadata (no file reads),
// then only reads files whose mtime changed to verify by content hash.
// This avoids both full-repo content hashing and false positives from
// touch-only mtime bumps.
func HasStaleFiles(ctx context.Context, db *DB) (bool, error) {
	// Quick check: has .gitignore changed? (separate metadata, not in files table)
	if checkGitignoreStale(db.Root()) {
		return true, nil
	}

	// Bulk-load all indexed metadata in two queries (not per-file).
	indexedMeta, err := db.GetAllFileMeta(ctx)
	if err != nil || len(indexedMeta) == 0 {
		return true, nil
	}
	indexedHashes, err := db.GetAllFileHashes(ctx)
	if err != nil {
		indexedHashes = make(map[string]string)
	}

	root := db.Root()
	gitignore := LoadGitIgnore(root)
	seen := make(map[string]bool, len(indexedMeta))
	var stale bool

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
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

		if GetLangConfig(path) == nil {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > 1<<20 {
			return nil
		}

		seen[path] = true
		storedMtime, inDB := indexedMeta[path]

		if !inDB {
			// New file not in index
			stale = true
			return filepath.SkipAll
		}
		if info.ModTime().UnixNano() <= storedMtime {
			// Mtime unchanged — skip (no read needed)
			return nil
		}

		// Mtime changed — verify by content hash to avoid false positive on touch.
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if fileHash(src) != indexedHashes[path] {
			stale = true
			return filepath.SkipAll
		}
		return nil
	})

	if stale {
		return true, nil
	}

	// Check for deleted files: any indexed path not seen on disk.
	// Only consider paths with a language config — non-source files
	// (e.g. legacy rows) in the DB should not trigger staleness.
	for path := range indexedMeta {
		if !seen[path] && GetLangConfig(path) != nil {
			return true, nil
		}
	}

	return false, nil
}

// fileCandidate holds everything needed to parse and index a file.
type fileCandidate struct {
	path   string
	src    []byte
	hash   string
	mtime  int64
	lang   *LangConfig
	isNew  bool
}

// parsedFile holds the parse result for a file candidate.
type parsedFile struct {
	candidate fileCandidate
	result    *ParseResult
	err       error // non-nil if parse failed
}

// ProgressFunc is called periodically during indexing with (filesIndexed, symbolsFound).
type ProgressFunc func(files, symbols int)

func IndexRepo(ctx context.Context, db *DB, progress ...ProgressFunc) (int, int, error) {
	var progressFn ProgressFunc
	if len(progress) > 0 {
		progressFn = progress[0]
	}
	root := db.Root()
	gitignore := LoadGitIgnore(root)

	if err := db.Prune(ctx); err != nil {
		return 0, 0, err
	}

	// Bulk-load all existing file hashes in one query instead of per-file lookups.
	existingHashes, err := db.GetAllFileHashes(ctx)
	if err != nil {
		existingHashes = make(map[string]string)
	}

	// Pipeline: walk → parse workers → DB writer.
	// Bounded channels limit peak memory to ~(buffer * max_file_size).
	candidates := make(chan fileCandidate, 64)
	parsed := make(chan parsedFile, 64)

	// Walk goroutine: produces candidates, skipping unchanged files.
	var walkErr error
	go func() {
		defer close(candidates)
		walkErr = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
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
			lang := GetLangConfig(path)
			if lang == nil {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.Size() > 1<<20 {
				return nil
			}

			src, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			hash := fileHash(src)
			_, existed := existingHashes[path]
			if existed && existingHashes[path] == hash {
				return nil
			}
			select {
			case candidates <- fileCandidate{
				path:  path,
				src:   src,
				hash:  hash,
				mtime: info.ModTime().UnixNano(),
				lang:  lang,
				isNew: !existed,
			}:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		})
	}()

	// Parse workers: read from candidates, write to parsed.
	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	var parseWg sync.WaitGroup
	parseWg.Add(workers)
	for range workers {
		go func() {
			defer parseWg.Done()
			for c := range candidates {
				result, err := ParseFileComplete(c.path, c.src, c.lang)
				parsed <- parsedFile{candidate: c, result: result, err: err}
			}
		}()
	}
	go func() {
		parseWg.Wait()
		close(parsed)
	}()

	// Writer: consume parsed results into a single batch transaction.
	if err := db.BeginBatch(ctx); err == nil {
		defer db.RollbackBatch()
	}

	var filesIndexed, symbolsFound int
	var fileErrors []FileError
	for pf := range parsed {
		c := &pf.candidate
		if pf.err != nil {
			fileErrors = append(fileErrors, FileError{File: c.path, Phase: "parse", Err: pf.err, Msg: pf.err.Error()})
			continue
		}
		if pf.result == nil {
			continue
		}
		if err := db.UpsertFile(ctx, c.path, c.hash, c.mtime); err != nil {
			fileErrors = append(fileErrors, FileError{File: c.path, Phase: "upsert", Err: err, Msg: err.Error()})
			continue
		}
		if !c.isNew {
			if err := db.ClearFileData(ctx, c.path); err != nil {
				fileErrors = append(fileErrors, FileError{File: c.path, Phase: "clear", Err: err, Msg: err.Error()})
				continue
			}
		}
		symbolIDs, err := db.InsertSymbolsBatch(ctx, pf.result.Symbols)
		if err != nil {
			fileErrors = append(fileErrors, FileError{File: c.path, Phase: "symbols", Err: err, Msg: err.Error()})
			continue
		}
		symbolsFound += len(pf.result.Symbols)
		if err := db.InsertImports(ctx, pf.result.Imports); err != nil {
			fileErrors = append(fileErrors, FileError{File: c.path, Phase: "imports", Err: err, Msg: err.Error()})
			continue
		}
		refs := pf.result.ExtractRefs(symbolIDs)
		if err := db.InsertRefs(ctx, c.path, refs); err != nil {
			fileErrors = append(fileErrors, FileError{File: c.path, Phase: "refs", Err: err, Msg: err.Error()})
			continue
		}
		filesIndexed++
		if progressFn != nil && filesIndexed%100 == 0 {
			progressFn(filesIndexed, symbolsFound)
		}
	}
	db.indexWarnings = fileErrors

	// Persist .gitignore metadata so HasStaleFiles can detect ignore-rule changes.
	persistGitignoreMeta(root)

	// Abort the transaction if the walk failed or context was cancelled.
	// The deferred RollbackBatch() handles cleanup.
	if walkErr != nil {
		return 0, 0, walkErr
	}
	if ctx.Err() != nil {
		return 0, 0, ctx.Err()
	}

	if err := db.CommitBatch(); err != nil {
		return filesIndexed, symbolsFound, fmt.Errorf("commit index batch: %w", err)
	}
	if err := updateIndexedSnapshot(ctx, root); err != nil {
		return filesIndexed, symbolsFound, err
	}

	return filesIndexed, symbolsFound, nil
}

// IndexFile re-indexes a single file, updating the DB with fresh symbols.
func IndexFile(ctx context.Context, db *DB, path string) error {
	path, err := db.ResolvePath(path)
	if err != nil {
		return err
	}

	lang := GetLangConfig(path)
	if lang == nil {
		return nil // unsupported language, nothing to index
	}

	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("indexfile: read: %w", err)
	}

	hash := fileHash(src)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("indexfile: stat: %w", err)
	}

	result, err := ParseFileComplete(path, src, lang)
	if err != nil {
		return fmt.Errorf("indexfile: parse: %w", err)
	}

	if err := db.UpsertFile(ctx, path, hash, info.ModTime().UnixNano()); err != nil {
		return err
	}
	if err := db.ClearFileData(ctx, path); err != nil {
		return err
	}

	symbolIDs, err := db.InsertSymbolsBatch(ctx, result.Symbols)
	if err != nil {
		return err
	}

	if err := db.InsertImports(ctx, result.Imports); err != nil {
		return err
	}

	refs := result.ExtractRefs(symbolIDs)
	if err := db.InsertRefs(ctx, path, refs); err != nil {
		return err
	}

	return RemoveIndexedSnapshot(db.Root())
}

// WalkRepoFiles calls fn for every non-ignored, non-binary file in the repo.
// It respects .gitignore and the always-ignored list, and skips files > 1MB.
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
	// Always skip .git and .edr
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
	return hex.EncodeToString(h[:4]) // first 8 hex chars
}

// repoMapConfig holds filters for RepoMap.
type repoMapConfig struct {
	dir        string // only include files under this directory
	glob       string // only include files matching this glob
	symbolType string // filter to this symbol type
	grep       string // only include symbols whose name contains this
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
func WithSymbolType(t string) RepoMapOption {
	return func(c *repoMapConfig) { c.symbolType = t }
}

// WithGrep filters repo-map to symbols whose name contains the given string.
func WithGrep(grep string) RepoMapOption {
	return func(c *repoMapConfig) { c.grep = grep }
}

// WithHideLocals filters out symbols that are nested inside functions/methods.
func WithHideLocals() RepoMapOption {
	return func(c *repoMapConfig) { c.hideLocals = true }
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
func RepoMap(ctx context.Context, db *DB, opts ...RepoMapOption) (string, bool, error) {
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
		return "", false, err
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

	// Sort files for budget-friendly output: non-test/bench first, shallower first, alpha
	sort.SliceStable(fileOrder, func(i, j int) bool {
		ri, _ := filepath.Rel(root, fileOrder[i])
		rj, _ := filepath.Rel(root, fileOrder[j])
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
			fmt.Fprintf(&b, "  %s %s [%d-%d]\n", sym.Type, sym.Name, sym.StartLine, sym.EndLine)
		}
		filesRendered++
		// Early stop: if budget set and exceeded, mark truncated and stop
		if budgetChars > 0 && b.Len() >= budgetChars {
			truncated = true
			break
		}
	}

	// Also mark truncated if we rendered fewer files than available
	if !truncated && budgetChars > 0 {
		totalFiles := 0
		for _, syms := range byFile {
			if len(syms) > 0 {
				totalFiles++
			}
		}
		if filesRendered < totalFiles {
			truncated = true
		}
	}

	return b.String(), truncated, nil
}

// isTestOrBenchFile returns true for common test/benchmark file patterns.
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
