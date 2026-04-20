// Package walk is the leaf package for gitignore-aware repo traversal.
//
// Callers that only need to enumerate files in a repo can import this
// package without dragging in parsers, resolvers, or the rest of
// internal/index. Behaviour (gitignore rules, binary/max-size filters,
// always-ignored directories) matches what was previously in
// internal/index.WalkRepoFiles.
package walk

import (
	"io/fs"
	"path/filepath"
)

// alwaysIgnore contains directories that are always skipped regardless of .gitignore.
var alwaysIgnore = []string{
	".git", ".claude",
}

// DefaultIgnore is the fallback ignore list used when no .gitignore exists.
var DefaultIgnore = []string{
	".git", ".claude", "node_modules", "vendor", "__pycache__",
	".venv", "venv", "target", "build", "dist", ".next",
	".idea", ".vscode", "bin", "obj",
}

// DirFiles walks a subdirectory of root, respecting .gitignore from root.
// Files larger than 1 MiB are skipped (matching the prior behaviour of
// internal/index.WalkDirFiles).
func DirFiles(root, dir string, fn func(path string) error) error {
	gitignore := LoadGitIgnore(root)
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
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

// RepoFiles walks every file in root that isn't filtered by .gitignore,
// the always-ignored directories, or the 1 MiB size cap. fn is called
// once per surviving file with its absolute path. This is the flat,
// non-fluent replacement for the former internal/index.WalkRepoFiles.
func RepoFiles(root string, fn func(path string) error) error {
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
	// Always skip .git and .claude.
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
		// No .gitignore — use hardcoded fallback.
		for _, ign := range DefaultIgnore {
			if name == ign {
				return true
			}
		}
	}
	return false
}
