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
	return filepath.Clean(abs), nil
}

// NormalizeRootOrDefault is like NormalizeRoot but returns a best-effort
// path on error (for use in non-critical contexts like lock file paths).
func NormalizeRootOrDefault(root string) string {
	r, err := NormalizeRoot(root)
	if err != nil {
		if root == "" || root == "." {
			wd, _ := os.Getwd()
			return wd
		}
		return root
	}
	return r
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
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	abs = filepath.Clean(abs)
	if !IsWithinRoot(root, abs) {
		return "", fmt.Errorf("path %q is outside repo root %s", abs, root)
	}
	return abs, nil
}
