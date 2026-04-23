package namespace

import (
	"os"
	"path/filepath"
	"strings"
)

// pythonCanonicalPathForFile returns the dotted module path for a
// Python file. The algorithm walks up from the file's directory,
// keeping each ancestor that contains __init__.py and stopping at
// the first ancestor that does NOT. Everything kept (in top-down
// order) plus the file's module name forms the canonical path.
//
//	/repo/foo/bar/baz.py   (init.py in foo/ AND foo/bar/)
//	  → foo.bar.baz
//	/repo/foo/bar/baz.py   (init.py only in foo/bar/, not in foo/)
//	  → bar.baz            (foo/ treated as the package root)
//	/repo/foo/bar/__init__.py (same)
//	  → bar
//	/repo/mod.py            (no init.py above)
//	  → mod
//
// Returns "" when the file lies outside repoRoot or isn't a .py/.pyi.
func pythonCanonicalPathForFile(file, repoRoot string) string {
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
	ext := filepath.Ext(rel)
	if ext != ".py" && ext != ".pyi" {
		return ""
	}
	stem := strings.TrimSuffix(rel, ext)

	parts := strings.Split(stem, "/")
	if len(parts) == 0 {
		return ""
	}
	last := parts[len(parts)-1]
	dirs := parts[:len(parts)-1]

	// Walk from leaf dir upward. Keep segments while __init__.py
	// exists; stop at the first ancestor without one.
	kept := []string{}
	for i := len(dirs); i > 0; i-- {
		dirAbs := filepath.Join(absRoot, filepath.FromSlash(strings.Join(dirs[:i], "/")))
		initPy := filepath.Join(dirAbs, "__init__.py")
		initPyi := filepath.Join(dirAbs, "__init__.pyi")
		hasInit := false
		if _, err := os.Stat(initPy); err == nil {
			hasInit = true
		} else if _, err := os.Stat(initPyi); err == nil {
			hasInit = true
		}
		if !hasInit {
			break
		}
		// Prepend: we're walking up but want top-down order.
		kept = append([]string{dirs[i-1]}, kept...)
	}

	if last == "__init__" {
		if len(kept) == 0 {
			return ""
		}
		return strings.Join(kept, ".")
	}
	return strings.Join(append(kept, last), ".")
}
