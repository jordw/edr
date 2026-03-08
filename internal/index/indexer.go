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
	"sort"
	"strings"
)

// alwaysIgnore contains directories that are always skipped regardless of .gitignore.
var alwaysIgnore = []string{
	".git", ".edr",
}

// DefaultIgnore is the fallback ignore list used when no .gitignore exists.
var DefaultIgnore = []string{
	".git", ".edr", "node_modules", "vendor", "__pycache__",
	".venv", "venv", "target", "build", "dist", ".next",
	".idea", ".vscode", "bin", "obj",
}

// IndexRepo indexes all supported files in the repository.
// HasStaleFiles checks whether the current repo snapshot differs from the
// snapshot persisted after indexing. If no snapshot exists yet, it falls back
// to the legacy mtime-based check.
func HasStaleFiles(ctx context.Context, db *DB) (bool, error) {
	if snap, ok, err := ReadIndexedSnapshot(db.Root()); err != nil {
		return false, err
	} else if ok {
		current, err := ComputeRepoSnapshot(ctx, db.Root())
		if err != nil {
			return false, err
		}
		return current.RootHash != snap.RootHash || current.FileCount != snap.FileCount, nil
	}

	return hasStaleFilesByMtime(ctx, db)
}

func hasStaleFilesByMtime(ctx context.Context, db *DB) (bool, error) {
	rows, err := db.db.QueryContext(ctx, "SELECT path, mtime FROM files LIMIT 100")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var path string
		var mtime int64
		if err := rows.Scan(&path, &mtime); err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return true, nil // file deleted = stale
		}
		if info.ModTime().Unix() > mtime {
			return true, nil
		}
	}
	return false, nil
}

func IndexRepo(ctx context.Context, db *DB) (int, int, error) {
	root := db.Root()
	gitignore := LoadGitIgnore(root)
	var filesIndexed, symbolsFound int

	if err := db.Prune(ctx); err != nil {
		return 0, 0, err
	}

	// Batch all SQLite writes into a single transaction for ~5x speedup.
	if err := db.BeginBatch(ctx); err == nil {
		defer db.RollbackBatch()
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Skip ignored directories
		if d.IsDir() {
			name := d.Name()
			if shouldIgnoreDir(name, path, root, gitignore) {
				return filepath.SkipDir
			}
			return nil
		}

		// Check .gitignore for files
		if gitignore != nil {
			rel, _ := filepath.Rel(root, path)
			if gitignore.IsIgnored(rel, false) {
				return nil
			}
		}

		// Skip non-supported files
		lang := GetLangConfig(path)
		if lang == nil {
			return nil
		}

		// Skip large files (>1MB)
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > 1<<20 {
			return nil
		}

		// Check if file needs re-indexing
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		hash := fileHash(src)
		storedHash, _ := db.GetFileHash(ctx, path)
		if storedHash == hash {
			return nil // already indexed, skip
		}

		// Parse and index with full import/ref extraction
		result, err := ParseFileComplete(path, src, lang)
		if err != nil {
			return nil // skip parse errors
		}

		// Update database
		if err := db.UpsertFile(ctx, path, hash, info.ModTime().Unix()); err != nil {
			return nil
		}
		if err := db.ClearFileData(ctx, path); err != nil {
			return nil
		}

		symbolIDs, err := db.InsertSymbolsBatch(ctx, result.Symbols)
		if err != nil {
			return nil
		}
		symbolsFound += len(result.Symbols)

		// Insert imports
		if err := db.InsertImports(ctx, result.Imports); err != nil {
			return nil
		}

		// Extract and insert refs
		refs := result.ExtractRefs(symbolIDs)
		if err := db.InsertRefs(ctx, path, refs); err != nil {
			return nil
		}

		filesIndexed++
		return nil
	})

	if err != nil {
		return filesIndexed, symbolsFound, err
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

	if err := db.UpsertFile(ctx, path, hash, info.ModTime().Unix()); err != nil {
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
func RepoMap(ctx context.Context, db *DB, opts ...RepoMapOption) (string, error) {
	cfg := repoMapConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	symbols, err := db.AllSymbols(ctx)
	if err != nil {
		return "", err
	}

	root := db.Root()

	// Group by file, applying filters
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

		// Dir filter
		if cfg.dir != "" {
			if !strings.HasPrefix(rel, cfg.dir+"/") && rel != cfg.dir {
				continue
			}
		}

		// Glob filter
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

		// Symbol type filter
		if cfg.symbolType != "" && s.Type != cfg.symbolType {
			continue
		}

		// Grep filter: try regex first, fall back to case-insensitive substring
		if cfg.grep != "" {
			if re, err := regexp.Compile("(?i)(?:" + cfg.grep + ")"); err == nil {
				if !re.MatchString(s.Name) {
					continue
				}
			} else if !strings.Contains(strings.ToLower(s.Name), strings.ToLower(cfg.grep)) {
				continue
			}
		}

		if _, seen := byFile[s.File]; !seen {
			fileOrder = append(fileOrder, s.File)
		}
		byFile[s.File] = append(byFile[s.File], s)
	}

	// Filter out locals (symbols nested inside functions/methods)
	if cfg.hideLocals {
		for file, syms := range byFile {
			// Collect function/method ranges
			type span struct{ start, end uint32 }
			var funcSpans []span
			for _, s := range syms {
				if s.Type == "function" || s.Type == "method" {
					funcSpans = append(funcSpans, span{s.StartLine, s.EndLine})
				}
			}
			// Filter: keep symbols not contained inside any function/method
			filtered := syms[:0]
			for _, s := range syms {
				isLocal := false
				if s.Type != "function" && s.Type != "method" {
					for _, fs := range funcSpans {
						if s.StartLine > fs.start && s.EndLine <= fs.end {
							isLocal = true
							break
						}
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
			return !ti // non-test first
		}
		di := strings.Count(ri, string(filepath.Separator))
		dj := strings.Count(rj, string(filepath.Separator))
		if di != dj {
			return di < dj // shallower first
		}
		return ri < rj
	})

	var b strings.Builder
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
	}

	return b.String(), nil
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
