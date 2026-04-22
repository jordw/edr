package index

import (
	"os"
	"path/filepath"
	"strings"
)

// importsReach returns true if the importing file's imports could reach the target file.
// This is a heuristic — not full module resolution — but eliminates most false positives.
func importsReach(imports []ImportInfo, targetFile, importingFile, root string) bool {
	if targetFile == importingFile {
		return true
	}

	langID := LangID(importingFile)
	targetLangID := LangID(targetFile)

	// Cross-language files cannot import each other.
	if langID != "" && targetLangID != "" && !langFamilyMatch(langID, targetLangID) {
		return false
	}

	// Same directory = same package in Go, same module scope in most languages
	if filepath.Dir(targetFile) == filepath.Dir(importingFile) {
		return true
	}

	if langID == "" {
		return false // unknown lang — deny rather than allow false positives
	}

	for _, imp := range imports {
		if importReachesFile(imp, targetFile, importingFile, root, langID) {
			return true
		}
	}
	return false
}

func importReachesFile(imp ImportInfo, targetFile, importingFile, root, langID string) bool {
	switch langID {
	case "go":
		return goImportReaches(imp, targetFile, root)
	case "javascript", "typescript":
		return jsImportReaches(imp, targetFile, importingFile, root)
	case "python":
		return pythonImportReaches(imp, targetFile, importingFile, root)
	case "java", "kotlin", "scala":
		return jvmImportReaches(imp, targetFile, root)
	case "rust":
		return rustImportReaches(imp, targetFile, importingFile)
	case "c", "cpp":
		return cIncludeReaches(imp, targetFile, importingFile)
	case "ruby":
		return rubyRequireReaches(imp, targetFile, importingFile, root)
	case "csharp":
		return csharpImportReaches(imp, targetFile, root)
	case "php":
		return phpImportReaches(imp, targetFile, root)
	case "swift":
		return swiftImportReaches(imp, targetFile, root)
	default:
		return false // unknown language — deny rather than allow false positives
	}
}

// goImportReaches checks if a Go import path could refer to the target file.
func goImportReaches(imp ImportInfo, targetFile, root string) bool {
	importPath := imp.ImportPath

	// Skip stdlib imports (no dots in path and no slashes typically means stdlib,
	// but more accurately: if it doesn't start with a domain-like prefix)
	if !strings.Contains(importPath, ".") && !strings.Contains(importPath, "/") {
		return false
	}

	// Get relative path of the target file from root
	rel, err := filepath.Rel(root, targetFile)
	if err != nil {
		return false
	}
	targetDir := filepath.Dir(rel)

	// Try to match: the import path suffix should match the target directory.
	// e.g., import "github.com/jordw/edr/internal/index" should match
	// a target file in internal/index/
	//
	// Strategy: check if the import path ends with the target directory
	// or if the target directory ends with a suffix of the import path
	importParts := strings.Split(importPath, "/")
	targetParts := strings.Split(filepath.ToSlash(targetDir), "/")

	// Try matching from the end
	minLen := len(importParts)
	if len(targetParts) < minLen {
		minLen = len(targetParts)
	}

	if minLen == 0 {
		return false
	}

	// Check if target dir ends with the import path suffix
	match := true
	for i := 0; i < minLen; i++ {
		if importParts[len(importParts)-1-i] != targetParts[len(targetParts)-1-i] {
			match = false
			break
		}
	}

	return match
}

// jsImportReaches checks if a JS/TS import could refer to the target file.
func jsImportReaches(imp ImportInfo, targetFile, importingFile, root string) bool {
	importPath := imp.ImportPath

	if strings.HasPrefix(importPath, ".") {
		// Relative import — resolve against the importing file's directory.
		importDir := filepath.Dir(importingFile)
		resolved := filepath.Join(importDir, importPath)
		if jsPathMatches(resolved, targetFile) {
			return true
		}
		return false
	}

	// Non-relative import (package name like "react" or "@scope/pkg/path").
	// In monorepos the package name often matches a directory under root.
	// Match if the target file lives under a directory whose name matches
	// the first segment of the import path (e.g. "react" → packages/react/).
	segments := strings.SplitN(importPath, "/", 2)
	pkgName := segments[0]
	if strings.HasPrefix(pkgName, "@") && len(segments) > 1 {
		// Scoped package: @scope/name
		rest := strings.SplitN(segments[1], "/", 2)
		pkgName = segments[0] + "/" + rest[0]
	}

	rel, err := filepath.Rel(root, targetFile)
	if err != nil {
		return false
	}
	relSlash := filepath.ToSlash(rel)

	// Check if the target path contains a directory matching the package name.
	// e.g. "react" matches "packages/react/src/jsx/ReactJSXElement.js"
	parts := strings.Split(relSlash, "/")
	for _, p := range parts {
		if p == pkgName || p == filepath.Base(pkgName) {
			return true
		}
	}

	return false
}

// jsPathMatches checks if a resolved JS/TS import path could refer to the target file,
// trying common extensions and index files.
func jsPathMatches(resolved, targetFile string) bool {
	extensions := []string{"", ".ts", ".tsx", ".js", ".jsx"}
	indexFiles := []string{"/index.ts", "/index.tsx", "/index.js", "/index.jsx"}
	targetClean := filepath.Clean(targetFile)

	for _, ext := range extensions {
		if filepath.Clean(resolved+ext) == targetClean {
			return true
		}
	}
	for _, idx := range indexFiles {
		if filepath.Clean(resolved+idx) == targetClean {
			return true
		}
	}
	return false
}

// jvmImportReaches checks if a Java/Kotlin/Scala import could refer to the target file.
// Import paths use dots (e.g., "java.util.List"). We match by converting to path segments
// and checking suffix match against the target file path.
func jvmImportReaches(imp ImportInfo, targetFile, root string) bool {
	importPath := imp.ImportPath

	// Wildcard import — match any file in the package
	if imp.Alias == "*" {
		// importPath is the package, e.g. "java.util.concurrent"
		parts := strings.Split(importPath, ".")
		return pathSuffixMatch(parts, targetFile, root)
	}

	// Specific import — last segment is the class name, preceding segments are the package
	parts := strings.Split(importPath, ".")
	if len(parts) < 2 {
		return false
	}
	// Match against directory of the package (all but last segment)
	return pathSuffixMatch(parts[:len(parts)-1], targetFile, root)
}

// csharpImportReaches checks if a C# using directive could refer to the target file.
// Using directives reference namespaces (e.g., "System.Collections.Generic").
func csharpImportReaches(imp ImportInfo, targetFile, root string) bool {
	parts := strings.Split(imp.ImportPath, ".")
	return pathSuffixMatch(parts, targetFile, root)
}

// phpImportReaches checks if a PHP use declaration could refer to the target file.
// Import paths use forward slashes (already converted from backslashes).
func phpImportReaches(imp ImportInfo, targetFile, root string) bool {
	importPath := imp.ImportPath
	// Last segment is the class name, preceding segments map to directory
	parts := strings.Split(importPath, "/")
	if len(parts) < 2 {
		return false
	}
	return pathSuffixMatch(parts[:len(parts)-1], targetFile, root)
}

// swiftImportReaches checks if a Swift import could refer to the target file.
// Swift imports are module-level (e.g., "Foundation", "UIKit.UIView").
// We match if the first segment matches a directory in the target path.
func swiftImportReaches(imp ImportInfo, targetFile, root string) bool {
	parts := strings.Split(imp.ImportPath, ".")
	return pathSuffixMatch(parts[:1], targetFile, root)
}

// pathSuffixMatch checks if the given path segments match as a suffix of the target file's directory.
func pathSuffixMatch(segments []string, targetFile, root string) bool {
	rel, err := filepath.Rel(root, targetFile)
	if err != nil {
		return false
	}
	targetParts := strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/")

	if len(segments) > len(targetParts) {
		return false
	}
	for i := 0; i < len(segments); i++ {
		if !strings.EqualFold(segments[len(segments)-1-i], targetParts[len(targetParts)-1-i]) {
			return false
		}
	}
	return true
}

// pythonImportReaches checks if a Python import could refer to the target file.
func pythonImportReaches(imp ImportInfo, targetFile, importingFile, root string) bool {
	importPath := imp.ImportPath

	// Handle relative imports
	if strings.HasPrefix(importPath, ".") {
		dots := 0
		for _, c := range importPath {
			if c == '.' {
				dots++
			} else {
				break
			}
		}
		remainder := importPath[dots:]
		baseDir := filepath.Dir(importingFile)
		for i := 1; i < dots; i++ {
			baseDir = filepath.Dir(baseDir)
		}
		if remainder == "" {
			// from . import something — matches files in the same package dir
			return filepath.Dir(targetFile) == baseDir
		}
		importPath = remainder
		// Convert dot-separated to path relative to baseDir
		parts := strings.Split(importPath, ".")
		resolved := filepath.Join(append([]string{baseDir}, parts...)...)

		targetClean := filepath.Clean(targetFile)
		// Check module file
		if filepath.Clean(resolved+".py") == targetClean {
			return true
		}
		// Check package __init__.py
		if filepath.Clean(filepath.Join(resolved, "__init__.py")) == targetClean {
			return true
		}
		// Check if target is in the resolved directory
		if strings.HasPrefix(targetClean, filepath.Clean(resolved)+string(os.PathSeparator)) {
			return true
		}
		return false
	}

	// Absolute import — convert dots to path components
	parts := strings.Split(importPath, ".")

	// Check if target path contains these parts as a suffix
	rel, err := filepath.Rel(root, targetFile)
	if err != nil {
		return false
	}

	// Strip .py extension
	rel = strings.TrimSuffix(rel, ".py")
	rel = strings.TrimSuffix(rel, "/__init__")
	relParts := strings.Split(filepath.ToSlash(rel), "/")

	// Check suffix match
	if len(parts) > len(relParts) {
		return false
	}

	match := true
	for i := 0; i < len(parts); i++ {
		if parts[len(parts)-1-i] != relParts[len(relParts)-1-i] {
			match = false
			break
		}
	}
	return match
}

// langFamilyMatch returns true if two language IDs belong to the same family
// (e.g. "c" and "cpp" are the same family, "javascript" and "typescript" are the same family).
func langFamilyMatch(a, b string) bool {
	return langFamily(a) == langFamily(b)
}

func langFamily(id string) string {
	switch id {
	case "c", "cpp":
		return "c"
	case "javascript", "typescript":
		return "js"
	case "java", "kotlin", "scala":
		return "jvm"
	default:
		return id
	}
}

// cIncludeReaches checks if a C/C++ #include could refer to the target file.
func cIncludeReaches(imp ImportInfo, targetFile, importingFile string) bool {
	includePath := imp.ImportPath
	if includePath == "" {
		return false
	}

	// Resolve relative to the including file's directory.
	dir := filepath.Dir(importingFile)
	resolved := filepath.Clean(filepath.Join(dir, includePath))
	if resolved == filepath.Clean(targetFile) {
		return true
	}

	// Also check if the include path matches the target's basename
	// (common for project headers included from various directories).
	return filepath.Base(includePath) == filepath.Base(targetFile)
}

// rubyRequireReaches checks if a Ruby require/require_relative could refer to the target file.
func rubyRequireReaches(imp ImportInfo, targetFile, importingFile, root string) bool {
	reqPath := imp.ImportPath

	// require_relative: the Alias field is set to "relative" by the Ruby parser
	// for require_relative calls. Otherwise, check if the path starts with "./"
	if imp.Alias == "relative" || strings.HasPrefix(reqPath, "./") || strings.HasPrefix(reqPath, "../") {
		dir := filepath.Dir(importingFile)
		resolved := filepath.Clean(filepath.Join(dir, reqPath))
		target := filepath.Clean(strings.TrimSuffix(targetFile, ".rb"))
		return resolved == target || resolved+".rb" == filepath.Clean(targetFile)
	}

	// require uses load path — match by suffix
	rel, err := filepath.Rel(root, targetFile)
	if err != nil {
		return false
	}
	rel = strings.TrimSuffix(rel, ".rb")
	return strings.HasSuffix(filepath.ToSlash(rel), reqPath)
}

// rustImportReaches maps a Rust import entry to a target file.
//
// Handles two forms:
//   - "mod:name" — a `mod name;` declaration. The target is either
//     `<dir>/name.rs` or `<dir>/name/mod.rs`, where <dir> is the
//     directory of the importing file. This is the high-signal case.
//   - "crate::...", "super::...", "self::...", plain paths — `use`
//     paths. Resolution requires knowing the crate root, which we do
//     not attempt here; returning false is conservative (produces
//     false negatives, not false positives). The scope-aware rename
//     path has a separate same-crate walker that catches these.
func rustImportReaches(imp ImportInfo, targetFile, importingFile string) bool {
	path := imp.ImportPath
	if !strings.HasPrefix(path, "mod:") {
		return false
	}
	name := strings.TrimPrefix(path, "mod:")
	dir := filepath.Dir(importingFile)
	// `dir/name.rs`
	if filepath.Clean(filepath.Join(dir, name+".rs")) == filepath.Clean(targetFile) {
		return true
	}
	// `dir/name/mod.rs`
	if filepath.Clean(filepath.Join(dir, name, "mod.rs")) == filepath.Clean(targetFile) {
		return true
	}
	return false
}
