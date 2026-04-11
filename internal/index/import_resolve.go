package index

import (
	"path/filepath"
	"strings"
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
		parts := strings.Split(filepath.ToSlash(rel), "/")
		// Index each suffix: "core.c", "sched/core.c", "kernel/sched/core.c"
		for i := len(parts) - 1; i >= 0; i-- {
			suffix := strings.Join(parts[i:], "/")
			idx[suffix] = append(idx[suffix], rel)
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
		// Go: raw is a module path like "github.com/foo/bar"
		// Try the last 1-3 components as directory names
		parts := strings.Split(raw, "/")
		for i := len(parts) - 1; i >= 0 && i >= len(parts)-3; i-- {
			suffix := strings.Join(parts[i:], "/")
			// Look for any .go file under this directory suffix
			for _, candidate := range idx[suffix] {
				if strings.HasSuffix(candidate, ".go") {
					return []string{candidate}
				}
			}
		}
		return nil

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
