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

	// Same directory = same package in Go, same module scope in most languages
	if filepath.Dir(targetFile) == filepath.Dir(importingFile) {
		return true
	}

	langID := LangID(importingFile)
	if langID == "" {
		return true // unknown lang, be permissive
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
	case "csharp":
		return csharpImportReaches(imp, targetFile, root)
	case "php":
		return phpImportReaches(imp, targetFile, root)
	case "swift":
		return swiftImportReaches(imp, targetFile, root)
	default:
		return true // unknown language, be permissive
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

	// Skip node_modules imports (no relative path prefix)
	if !strings.HasPrefix(importPath, ".") {
		return false
	}

	// Resolve relative to importing file
	importDir := filepath.Dir(importingFile)
	resolved := filepath.Join(importDir, importPath)

	// Try various extensions
	extensions := []string{"", ".ts", ".tsx", ".js", ".jsx"}
	indexFiles := []string{"/index.ts", "/index.tsx", "/index.js", "/index.jsx"}

	targetClean := filepath.Clean(targetFile)

	for _, ext := range extensions {
		candidate := filepath.Clean(resolved + ext)
		if candidate == targetClean {
			return true
		}
	}

	for _, idx := range indexFiles {
		candidate := filepath.Clean(resolved + idx)
		if candidate == targetClean {
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
