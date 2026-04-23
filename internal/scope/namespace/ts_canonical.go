package namespace

import (
	"path/filepath"
	"strings"
)

// tsCanonicalPathForFile returns the canonical module path for a
// TS/JS file. Convention: repo-root-relative path with any
// TS/JS/JSX/DTS extension stripped.
//
//	/repo/src/foo/bar.ts   → src/foo/bar  (when repoRoot=/repo)
//	/repo/src/index.tsx    → src/index
//	/repo/lib/foo/index.js → lib/foo/index
//
// Returns "" when the file lies outside repoRoot or lacks a TS/JS
// extension. `index.ts` is NOT collapsed to its directory — we treat
// module-resolution index-lookup as the populator's job, keeping the
// canonical 1-to-1 with the file so sibling `foo.ts` vs
// `foo/index.ts` don't collide.
func tsCanonicalPathForFile(file, repoRoot string) string {
	absFile, err := filepath.Abs(file)
	if err != nil {
		return ""
	}
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(absRoot, absFile)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	rel = filepath.ToSlash(rel)
	for _, ext := range []string{".d.ts", ".tsx", ".ts", ".jsx", ".js", ".mjs", ".cjs"} {
		if strings.HasSuffix(rel, ext) {
			return strings.TrimSuffix(rel, ext)
		}
	}
	return ""
}
