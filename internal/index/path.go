package index

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NormalizeRoot returns a cleaned absolute repository root path.
func NormalizeRoot(root string) (string, error) {
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("normalize root: %w", err)
	}
	// Resolve symlinks so that /tmp → /private/tmp (macOS) and similar
	// symlinked paths produce the same root regardless of entry point.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return filepath.Clean(abs), nil
	}
	return filepath.Clean(resolved), nil
}

// IsWithinRoot reports whether path is inside root.
func IsWithinRoot(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

// ResolvePath converts path to an absolute path and rejects anything outside root.
func ResolvePath(root, path string) (string, error) {
	return resolvePathInner(root, path, false)
}

// ResolvePathReadOnly is like ResolvePath but allows absolute paths outside the
// repo root. Relative paths that escape the root are still rejected.
// Agent sandbox security is handled by the agent harness, not edr.
func ResolvePathReadOnly(root, path string) (string, error) {
	return resolvePathInner(root, path, true)
}

func resolvePathInner(root, path string, allowAbsOutside bool) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	isAbs := filepath.IsAbs(path)
	if !isAbs {
		path = filepath.Join(root, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	abs = filepath.Clean(abs)
	// Resolve symlinks to match the resolved root from NormalizeRoot.
	// If the full path doesn't exist (e.g., dry-run write of new file),
	// resolve the parent directory and re-append the basename.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	} else if resolved, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		abs = filepath.Join(resolved, filepath.Base(abs))
	}
	if !IsWithinRoot(root, abs) {
		if allowAbsOutside && isAbs {
			return abs, nil
		}
		return "", fmt.Errorf("path %q is outside repo root %s", abs, root)
	}
	return abs, nil
}
