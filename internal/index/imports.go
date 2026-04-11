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

	// Go: handled by extractGoImports (supports import blocks)
	".go": nil,

	// Python: from X import Y, import X
	".py": {
		regexp.MustCompile(`from\s+([\w.]+)\s+import`),
		regexp.MustCompile(`(?m)^import\s+([\w.]+)`),
	},

	// JavaScript/TypeScript: import ... from 'path', require('path')
	".js":  {regexp.MustCompile(`(?:from|require\s*\()\s*['"]([^'"]+)['"]`)},
	".ts":  {regexp.MustCompile(`(?:from|require\s*\()\s*['"]([^'"]+)['"]`)},
	".tsx": {regexp.MustCompile(`(?:from|require\s*\()\s*['"]([^'"]+)['"]`)},
	".jsx": {regexp.MustCompile(`(?:from|require\s*\()\s*['"]([^'"]+)['"]`)},

	// Rust: use path::to::module
	".rs": {regexp.MustCompile(`use\s+([\w:]+)`)},

	// Ruby: require 'path' and require_relative 'path'
	".rb": {
		regexp.MustCompile(`require\s+['"]([^'"]+)['"]`),
		regexp.MustCompile(`require_relative\s+['"]([^'"]+)['"]`),
	},

	// Java: import package.Class and import static package.Class.method
	".java": {regexp.MustCompile(`import\s+(?:static\s+)?([\w.]+)`)},
}

// ExtractImports extracts import/include statements from source code.
// Returns raw import strings (not resolved to file paths).
func ExtractImports(src []byte, ext string) []ImportEntry {
	ext = strings.ToLower(ext)

	// Language-specific extractors for languages with block syntax
	switch ext {
	case ".go":
		return extractGoImports(src)
	case ".rs":
		return extractRustImports(src)
	}

	patterns, ok := importPatterns[ext]
	if !ok || patterns == nil {
		return nil
	}

	// Only scan the first 200 lines (imports are at the top)
	chunk := scanHead(src, 200)

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

// scanHead returns the first N lines of src.
func scanHead(src []byte, lines int) []byte {
	newlines := 0
	for i, b := range src {
		if b == '\n' {
			newlines++
			if newlines >= lines {
				return src[:i]
			}
		}
	}
	return src
}

// extractGoImports handles both single-line and block imports:
//
//	import "fmt"
//	import (
//	    "os"
//	    foo "github.com/foo/bar"
//	)
func extractGoImports(src []byte) []ImportEntry {
	chunk := scanHead(src, 200)
	var imports []ImportEntry
	seen := map[string]bool{}

	// Match quoted strings inside import statements/blocks
	re := regexp.MustCompile(`"([^"]+)"`)
	lines := strings.Split(string(chunk), "\n")
	inBlock := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import (") {
			inBlock = true
			continue
		}
		if inBlock && trimmed == ")" {
			inBlock = false
			continue
		}
		if inBlock || strings.HasPrefix(trimmed, "import ") {
			// Extract quoted path from this line
			if m := re.FindStringSubmatch(trimmed); len(m) >= 2 {
				raw := m[1]
				if raw != "" && !seen[raw] {
					seen[raw] = true
					imports = append(imports, ImportEntry{Raw: raw})
				}
			}
		}
	}
	return imports
}

// extractRustImports handles use statements and mod declarations:
//
//	use std::collections::HashMap;
//	use tokio::runtime::{self, Runtime};
//	mod config;
func extractRustImports(src []byte) []ImportEntry {
	chunk := scanHead(src, 200)
	var imports []ImportEntry
	seen := map[string]bool{}

	useRe := regexp.MustCompile(`(?m)^use\s+([\w:]+)`)
	modRe := regexp.MustCompile(`(?m)^mod\s+(\w+)\s*;`)

	for _, re := range []*regexp.Regexp{useRe, modRe} {
		for _, match := range re.FindAllSubmatch(chunk, -1) {
			if len(match) < 2 {
				continue
			}
			raw := string(match[1])
			if raw != "" && !seen[raw] {
				seen[raw] = true
				imports = append(imports, ImportEntry{Raw: raw})
			}
		}
	}
	return imports
}
