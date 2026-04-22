package namespace

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// rustCrateInfo caches what we know about a Rust crate root: the
// directory containing Cargo.toml and the crate name declared in
// [package]. Canonical paths for decls in this crate are
// <crateName>::<mod path from src/>.
type rustCrateInfo struct {
	root      string // absolute path to dir containing Cargo.toml
	crateName string // from [package] name = "..."
}

// rustCanonicalPathCache caches crate lookups and canonical paths.
// Keys for the dirs map are absolute directory paths that have been
// resolved to a crate root (or determined to be outside any crate).
type rustCanonicalPathCache struct {
	mu   sync.Mutex
	dirs map[string]*rustCrateInfo // dir → crate info (nil ⇒ outside any crate)
}

func newRustCanonicalPathCache() *rustCanonicalPathCache {
	return &rustCanonicalPathCache{dirs: make(map[string]*rustCrateInfo)}
}

// CanonicalPathForRustFile returns the canonical module path for a
// .rs file. Conventions:
//
//	src/lib.rs           → <crate>
//	src/main.rs          → <crate>
//	src/foo.rs           → <crate>::foo
//	src/foo/mod.rs       → <crate>::foo
//	src/foo/bar.rs       → <crate>::foo::bar
//
// Returns "" when the file is not under the crate's src/ directory
// (benches, tests, build.rs) or Cargo.toml cannot be found. Callers
// fall back to the file-path-based DeclID when the canonical path
// is empty.
func (c *rustCanonicalPathCache) CanonicalPathForRustFile(file string) string {
	abs, err := filepath.Abs(file)
	if err != nil {
		return ""
	}
	dir := filepath.Dir(abs)
	info := c.crateInfo(dir)
	if info == nil {
		return ""
	}
	// Module path computation only applies under src/.
	srcDir := filepath.Join(info.root, "src")
	rel, err := filepath.Rel(srcDir, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	rel = filepath.ToSlash(rel)
	// Strip .rs extension.
	rel = strings.TrimSuffix(rel, ".rs")
	parts := strings.Split(rel, "/")
	// lib.rs / main.rs at the crate root → <crate>.
	if len(parts) == 1 && (parts[0] == "lib" || parts[0] == "main") {
		return info.crateName
	}
	// mod.rs in a subdir → path is the directory chain.
	if len(parts) > 0 && parts[len(parts)-1] == "mod" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return info.crateName
	}
	return info.crateName + "::" + strings.Join(parts, "::")
}

// crateInfo walks up from dir until it finds a Cargo.toml, caching
// every ancestor in the chain so a single discovery populates all
// descendants. Returns nil when no Cargo.toml exists above dir.
func (c *rustCanonicalPathCache) crateInfo(dir string) *rustCrateInfo {
	c.mu.Lock()
	defer c.mu.Unlock()

	if info, ok := c.dirs[dir]; ok {
		return info
	}
	var chain []string
	cur := dir
	for {
		chain = append(chain, cur)
		if info, ok := c.dirs[cur]; ok {
			for _, d := range chain {
				c.dirs[d] = info
			}
			return info
		}
		cargo := filepath.Join(cur, "Cargo.toml")
		if _, err := os.Stat(cargo); err == nil {
			info := readCargoToml(cur, cargo)
			for _, d := range chain {
				c.dirs[d] = info
			}
			return info
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			for _, d := range chain {
				c.dirs[d] = nil
			}
			return nil
		}
		cur = parent
	}
}

// readCargoToml extracts the package name from a Cargo.toml. Minimal
// TOML parsing: finds the [package] table and the `name = "..."`
// entry inside it. Workspace-only Cargo.toml files (no [package])
// return nil so descendants fall through to their own crate root.
func readCargoToml(rootDir, cargoPath string) *rustCrateInfo {
	f, err := os.Open(cargoPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	inPackage := false
	var name string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inPackage = line == "[package]"
			continue
		}
		if !inPackage {
			continue
		}
		if !strings.HasPrefix(line, "name") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		val := strings.TrimSpace(line[eq+1:])
		// Strip inline comments.
		if i := strings.IndexByte(val, '#'); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		val = strings.Trim(val, `"'`)
		if val != "" {
			name = val
			break
		}
	}
	if name == "" {
		return nil
	}
	return &rustCrateInfo{root: rootDir, crateName: name}
}
