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

	lang := GetLangConfig(importingFile)
	if lang == nil {
		return true // unknown lang, be permissive
	}

	for _, imp := range imports {
		if importReachesFile(imp, targetFile, importingFile, root, lang.LangID) {
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
