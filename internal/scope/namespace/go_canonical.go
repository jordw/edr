package namespace

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// goModInfo caches what we know about a Go module root: the directory
// containing go.mod and the module path declared in it. Canonical paths
// for decls in this module are <modulePath>/<relativeDir>.
type goModInfo struct {
	root       string // absolute path to dir containing go.mod
	modulePath string // e.g., "github.com/jordw/edr"
}

// goCanonicalPathCache caches goModInfo lookups so every file in a
// module only walks up to go.mod once per process. Keys are absolute
// directory paths that have been resolved (both the module root and
// its descendants map to the same goModInfo).
type goCanonicalPathCache struct {
	mu   sync.Mutex
	dirs map[string]*goModInfo // dir → info (nil ⇒ not in a module)
}

func newGoCanonicalPathCache() *goCanonicalPathCache {
	return &goCanonicalPathCache{dirs: make(map[string]*goModInfo)}
}

// CanonicalPathForGoFile returns the canonical path prefix for a Go
// file — the namespace in which its file-scope decls live. For
// "<moduleRoot>/foo/bar/baz.go" with module "example.com/m", returns
// "example.com/m/foo/bar". Returns "" when go.mod cannot be found.
//
// The returned string is the scope-builder canonicalPath input that
// makes file-scope DeclIDs identity-equal across files in the same
// package.
func (c *goCanonicalPathCache) CanonicalPathForGoFile(file string) string {
	abs, err := filepath.Abs(file)
	if err != nil {
		return ""
	}
	dir := filepath.Dir(abs)
	info := c.moduleInfo(dir)
	if info == nil {
		return ""
	}
	rel, err := filepath.Rel(info.root, dir)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == "" {
		return info.modulePath
	}
	return info.modulePath + "/" + rel
}

// moduleInfo walks up from dir until it finds a go.mod, caching
// results along the way. Returns nil when no go.mod exists above dir.
func (c *goCanonicalPathCache) moduleInfo(dir string) *goModInfo {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Fast path: already resolved.
	if info, ok := c.dirs[dir]; ok {
		return info
	}

	// Walk up collecting directories that will share the same answer,
	// so a single go.mod discovery populates the entire path.
	var chain []string
	cur := dir
	for {
		chain = append(chain, cur)
		if info, ok := c.dirs[cur]; ok {
			// An ancestor was already resolved — propagate downward.
			for _, d := range chain {
				c.dirs[d] = info
			}
			return info
		}
		modPath := filepath.Join(cur, "go.mod")
		if st, err := os.Stat(modPath); err == nil && !st.IsDir() {
			info := readGoMod(cur, modPath)
			for _, d := range chain {
				c.dirs[d] = info
			}
			return info
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Root reached without go.mod. Cache negatives.
			for _, d := range chain {
				c.dirs[d] = nil
			}
			return nil
		}
		cur = parent
	}
}

// readGoMod parses the `module <path>` line out of go.mod. A proper
// parser would use golang.org/x/mod/modfile, but we only need the
// module line — a single-pass text scan is enough and keeps this
// package dependency-free.
func readGoMod(rootDir, modPath string) *goModInfo {
	f, err := os.Open(modPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "module") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "module"))
		// Handle inline comments and quoted paths.
		if i := strings.IndexAny(rest, " \t/"); i >= 0 && strings.HasPrefix(rest[i:], "//") {
			rest = strings.TrimSpace(rest[:i])
		}
		rest = strings.Trim(rest, "\"")
		if rest == "" {
			continue
		}
		return &goModInfo{root: rootDir, modulePath: rest}
	}
	return nil
}
