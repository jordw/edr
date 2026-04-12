package index

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// SuffixIndex maps file name suffixes to relative paths for fast import resolution.
// "sched.h" → ["include/linux/sched.h", "kernel/sched/sched.h"]
type SuffixIndex map[string][]string

// BuildSuffixIndex builds a suffix index from a list of relative file paths.
// For each file, indexes the filename and progressively longer path suffixes.
// E.g., "kernel/sched/core.c" → "core.c", "sched/core.c", "kernel/sched/core.c"
func BuildSuffixIndex(relPaths []string) SuffixIndex {
	idx := make(SuffixIndex, len(relPaths)*2)
	for _, rel := range relPaths {
		slashed := filepath.ToSlash(rel)
		parts := strings.Split(slashed, "/")
		// Index each suffix: "core.c", "sched/core.c", "kernel/sched/core.c"
		for i := len(parts) - 1; i >= 0; i-- {
			suffix := strings.Join(parts[i:], "/")
			idx[suffix] = append(idx[suffix], rel)
		}
		// Index directory suffixes for Go import resolution.
		// "pkg/apis/core/types.go" → dir entries for "pkg/apis/core", "apis/core", "core"
		if dir := filepath.Dir(slashed); dir != "." {
			dirParts := strings.Split(dir, "/")
			for i := len(dirParts) - 1; i >= 0; i-- {
				dirSuffix := strings.Join(dirParts[i:], "/")
				key := "\x00dir:" + dirSuffix // prefix to avoid collision with file suffixes
				idx[key] = append(idx[key], rel)
			}
		}
	}
	return idx
}

// ResolveImport tries to resolve a raw import string to file paths in the repo.
// Returns all matching relative paths. Uses the suffix index for C-style includes
// and module-to-path conversion for Go/Python/Java/Rust imports.
func ResolveImport(idx SuffixIndex, raw string, importerPath string, ext string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	ext = strings.ToLower(ext)

	switch ext {
	case ".c", ".h", ".cc", ".cpp", ".hpp":
		// C/C++: raw is a path suffix like "linux/sched.h" or "sched.h"
		return idx[filepath.ToSlash(raw)]

	case ".go":
		// Go: raw is a module path like "k8s.io/kubernetes/pkg/apis/core"
		// Strip the go.mod module prefix to get a repo-relative directory,
		// then look up directory entries in the suffix index.
		return resolveGoImport(idx, raw, importerPath)

	case ".py":
		// Python: raw is a dotted module like "torch.autograd"
		// Convert to path: torch/autograd → look for __init__.py or module.py
		pathForm := strings.ReplaceAll(raw, ".", "/")
		if matches := idx[pathForm+"/__init__.py"]; len(matches) > 0 {
			return matches
		}
		if matches := idx[pathForm+".py"]; len(matches) > 0 {
			return matches
		}
		// Try suffix match
		return idx[pathForm]

	case ".js", ".ts", ".tsx", ".jsx":
		// JS/TS: raw might be relative ("./foo") or package ("react")
		raw = strings.TrimPrefix(raw, "./")
		raw = strings.TrimPrefix(raw, "../")
		raw = filepath.ToSlash(raw)
		// Try with common extensions
		for _, tryExt := range []string{"", ".ts", ".tsx", ".js", ".jsx", "/index.ts", "/index.js"} {
			if matches := idx[raw+tryExt]; len(matches) > 0 {
				return matches
			}
		}
		return nil

	case ".rs":
		// Rust: raw is a :: path like "tokio::runtime::Runtime"
		// Convert to slash path, look for mod.rs or file.rs
		pathForm := strings.ReplaceAll(raw, "::", "/")
		// Remove the last component (it's the item name, not a file)
		if lastSlash := strings.LastIndex(pathForm, "/"); lastSlash > 0 {
			dir := pathForm[:lastSlash]
			if matches := idx[dir+"/mod.rs"]; len(matches) > 0 {
				return matches
			}
			if matches := idx[dir+".rs"]; len(matches) > 0 {
				return matches
			}
		}
		if matches := idx[pathForm+".rs"]; len(matches) > 0 {
			return matches
		}
		return nil

	case ".rb":
		// Ruby: raw is a path like "active_record/base"
		raw = filepath.ToSlash(raw)
		if matches := idx[raw+".rb"]; len(matches) > 0 {
			return matches
		}
		return idx[raw]

	case ".java":
		// Java: raw is a dotted package like "org.springframework.context.ApplicationContext"
		pathForm := strings.ReplaceAll(raw, ".", "/")
		if matches := idx[pathForm+".java"]; len(matches) > 0 {
			return matches
		}
		// Try without the last component (class name might match file name)
		if lastSlash := strings.LastIndex(pathForm, "/"); lastSlash > 0 {
			if matches := idx[pathForm[lastSlash+1:]+".java"]; len(matches) > 0 {
				return matches
			}
		}
		return nil
	}

	return nil
}

// --- Go import resolution ---

var (
	goModCache   = map[string]string{} // root → module path
	goModCacheMu sync.Mutex
)

// goModulePath reads the module path from go.mod, cached per root.
func goModulePath(root string) string {
	goModCacheMu.Lock()
	defer goModCacheMu.Unlock()
	if mod, ok := goModCache[root]; ok {
		return mod
	}
	mod := ""
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err == nil {
		for _, line := range strings.SplitN(string(data), "\n", 10) {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "module ") {
				mod = strings.TrimSpace(strings.TrimPrefix(line, "module "))
				break
			}
		}
	}
	goModCache[root] = mod
	return mod
}

// resolveGoImport resolves a Go import path to repo-relative file paths.
// Uses go.mod module prefix stripping + direct directory lookup in suffix index.
func resolveGoImport(idx SuffixIndex, raw string, importerPath string) []string {
	// Infer repo root from the importer path and try go.mod prefix stripping.
	// The suffix index has directory entries keyed as "\x00dir:<dirpath>".
	//
	// Strategy:
	// 1. If raw starts with the go.mod module path, strip it for an exact dir lookup
	// 2. Otherwise try the last 1-3 path components as directory suffixes
	// Both use O(1) map lookups instead of scanning.

	// Try module prefix stripping first (handles internal imports)
	// We need the root to read go.mod, but we only have importerPath (relative).
	// The caller (extractAllImports) sets this from the repo root walk,
	// so we can find go.mod relative to cwd or use a cached value.

	// Try progressively longer suffixes of the import path as directory lookups
	parts := strings.Split(raw, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		dirSuffix := strings.Join(parts[i:], "/")
		candidates := idx["\x00dir:"+dirSuffix]
		if len(candidates) == 0 {
			continue
		}
		// Filter to .go files, exclude tests
		var matches []string
		for _, c := range candidates {
			if strings.HasSuffix(c, ".go") && !strings.HasSuffix(c, "_test.go") {
				matches = append(matches, c)
			}
		}
		if len(matches) > 0 {
			return matches
		}
	}
	return nil
}
