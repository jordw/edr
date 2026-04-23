package namespace

import (
	"path/filepath"
	"strings"
)

// csharpCanonicalPathForFile returns the canonical path for a C# file.
// Convention: repo-root-relative path with the language extension
// stripped. This is intentionally minimal — it maps .c/.h-style
// sibling pairs by basename but does NOT canonicalize across
// package / namespace declarations inside the file.
func csharpCanonicalPathForFile(file, repoRoot string) string {
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
	for _, ext := range []string{".cs"} {
		if strings.HasSuffix(rel, ext) {
			return strings.TrimSuffix(rel, ext)
		}
	}
	return ""
}
