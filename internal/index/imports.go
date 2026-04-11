package index

import (
	"regexp"
	"strings"
)

// ImportEntry represents one import statement found in a source file.
type ImportEntry struct {
	Raw string // the raw import string as written (e.g., "sched.h", "torch.autograd")
}

// Language-specific import patterns. Each returns the imported path/module.
var importPatterns = map[string][]*regexp.Regexp{
	// C/C++: #include "foo.h" or #include <foo.h>
	".c": {regexp.MustCompile(`#include\s*["<]([^">]+)[">]`)},
	".h": {regexp.MustCompile(`#include\s*["<]([^">]+)[">]`)},
	".cc": {regexp.MustCompile(`#include\s*["<]([^">]+)[">]`)},
	".cpp": {regexp.MustCompile(`#include\s*["<]([^">]+)[">]`)},
	".hpp": {regexp.MustCompile(`#include\s*["<]([^">]+)[">]`)},

	// Go: import "path" or import name "path"
	".go": {regexp.MustCompile(`import\s+(?:\w+\s+)?"([^"]+)"`)},

	// Python: from X import Y, import X
	".py": {
		regexp.MustCompile(`from\s+([\w.]+)\s+import`),
		regexp.MustCompile(`^import\s+([\w.]+)`),
	},

	// JavaScript/TypeScript: import ... from 'path', require('path')
	".js":  {regexp.MustCompile(`(?:from|require\s*\()\s*['"]([^'"]+)['"]`)},
	".ts":  {regexp.MustCompile(`(?:from|require\s*\()\s*['"]([^'"]+)['"]`)},
	".tsx": {regexp.MustCompile(`(?:from|require\s*\()\s*['"]([^'"]+)['"]`)},
	".jsx": {regexp.MustCompile(`(?:from|require\s*\()\s*['"]([^'"]+)['"]`)},

	// Rust: use path::to::module
	".rs": {regexp.MustCompile(`use\s+([\w:]+)`)},

	// Ruby: require 'path'
	".rb": {regexp.MustCompile(`require\s+['"]([^'"]+)['"]`)},

	// Java: import package.Class
	".java": {regexp.MustCompile(`import\s+([\w.]+)`)},
}

// ExtractImports extracts import/include statements from source code.
// Returns raw import strings (not resolved to file paths).
func ExtractImports(src []byte, ext string) []ImportEntry {
	ext = strings.ToLower(ext)
	patterns, ok := importPatterns[ext]
	if !ok {
		return nil
	}

	// Only scan the first 200 lines (imports are at the top)
	scanLimit := len(src)
	newlines := 0
	for i, b := range src {
		if b == '\n' {
			newlines++
			if newlines >= 200 {
				scanLimit = i
				break
			}
		}
	}
	chunk := src[:scanLimit]

	var imports []ImportEntry
	seen := map[string]bool{}
	for _, re := range patterns {
		for _, match := range re.FindAllSubmatch(chunk, -1) {
			if len(match) < 2 {
				continue
			}
			raw := string(match[1])
			if raw == "" || seen[raw] {
				continue
			}
			seen[raw] = true
			imports = append(imports, ImportEntry{Raw: raw})
		}
	}
	return imports
}
