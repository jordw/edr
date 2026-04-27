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
	"os"
	"path/filepath"
	"strings"
)

// alwaysIgnore contains repo metadata names that are always skipped regardless of .gitignore.
var alwaysIgnore = []string{
	".git", ".edr", ".claude",
}

// IsAlwaysIgnoredPath reports whether rel contains a path segment in
// alwaysIgnore (.git, .edr, .claude). Used by callers — notably the
// symbol index — to prune stale entries that point into directories
// the walker no longer visits, so reports like `edr orient` don't
// surface .claude/worktrees files just because they were indexed
// before the ignore policy covered them.
func IsAlwaysIgnoredPath(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		for _, ign := range alwaysIgnore {
			if seg == ign {
				return true
			}
		}
	}
	return false
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
		if shouldIgnoreFile(d.Name()) {
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
		if shouldIgnoreFile(d.Name()) {
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
	// Always skip edr/git internals and agent worktree containers.
	for _, ign := range alwaysIgnore {
		if name == ign {
			return true
		}
	}
	// Nested git repositories/worktrees have their own root, index, session,
	// and checkpoint state. Do not index them as ordinary child directories
	// of the parent repo. The root itself is allowed to have a .git file
	// because linked worktrees use that shape.
	if filepath.Clean(path) != filepath.Clean(root) {
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
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

func shouldIgnoreFile(name string) bool {
	for _, ign := range alwaysIgnore {
		if name == ign {
			return true
		}
	}
	return false
}
