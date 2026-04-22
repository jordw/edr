package namespace

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/rust"
)

// RustResolver is a Resolver for repositories rooted at a Rust crate.
// File-scope decls hash with a canonical module path of the form
// <crate>::<mod path>, so a `pub fn foo` in src/a.rs and a ref via
// `use crate::a::foo` in src/b.rs bind to the same DeclID.
//
// v1 scope:
//   - Intra-crate `use` resolution only. External crate imports
//     (e.g., `use tokio::runtime::Handle` from outside tokio) are
//     not resolved; they'd require a crate-graph walk of Cargo.lock.
//   - Glob imports (`use foo::*`) are not surfaced.
//   - Module file resolution uses the standard layout: `mod foo;`
//     in the parent resolves to `foo.rs` or `foo/mod.rs`. Non-
//     standard #[path] overrides are not honored.
type RustResolver struct {
	repoRoot   string
	canonCache *rustCanonicalPathCache
	parseMu    sync.Mutex
	parseCache map[string]*scope.Result
	srcCache   map[string][]byte
	// fileByModule caches the module-path → file mapping for each
	// crate root that's been walked.
	walkMu       sync.Mutex
	crateWalks   map[string]map[string]string // crateRoot → canonicalPath → file
	sameCrateMap map[string][]string          // crateRoot → all .rs files under src/
}

// NewRustResolver constructs a resolver rooted at repoRoot.
func NewRustResolver(repoRoot string) *RustResolver {
	return &RustResolver{
		repoRoot:     repoRoot,
		canonCache:   newRustCanonicalPathCache(),
		parseCache:   make(map[string]*scope.Result),
		srcCache:     make(map[string][]byte),
		crateWalks:   make(map[string]map[string]string),
		sameCrateMap: make(map[string][]string),
	}
}

// Source returns the cached source bytes for a file.
func (r *RustResolver) Source(file string) []byte {
	r.parseMu.Lock()
	if b, ok := r.srcCache[file]; ok {
		r.parseMu.Unlock()
		return b
	}
	r.parseMu.Unlock()
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	r.parseMu.Lock()
	r.srcCache[file] = b
	r.parseMu.Unlock()
	return b
}

// CanonicalPath returns the canonical module path for a Rust file.
func (r *RustResolver) CanonicalPath(file string) string {
	return r.canonCache.CanonicalPathForRustFile(file)
}

// Result returns the parsed scope.Result for a Rust file, using
// ParseCanonical so file-scope DeclIDs are canonical.
func (r *RustResolver) Result(file string) *scope.Result {
	if strings.ToLower(filepath.Ext(file)) != ".rs" {
		return nil
	}
	r.parseMu.Lock()
	if res, ok := r.parseCache[file]; ok {
		r.parseMu.Unlock()
		return res
	}
	r.parseMu.Unlock()

	src, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	canonical := r.CanonicalPath(file)
	res := rust.ParseCanonical(file, canonical, src)

	r.parseMu.Lock()
	r.parseCache[file] = res
	r.srcCache[file] = src
	r.parseMu.Unlock()
	return res
}

// FilesForImport resolves a `use` path to the file that owns the
// module containing the imported item. For `use crate::runtime::Handle`:
//
//	input: "crate::runtime::Handle", importingFile: ".../src/task.rs"
//	output: [".../src/runtime.rs"] or [".../src/runtime/mod.rs"]
//
// The caller (populator) treats the returned files' file-scope decls
// as candidate import targets. The final path segment is the item
// name (not a file), so we resolve up to its parent module.
//
// Returns nil for external crate imports, `self`/`super` relative
// paths not yet supported, or malformed specs.
func (r *RustResolver) FilesForImport(importSpec, importingFile string) []string {
	info := r.crateInfoForFile(importingFile)
	if info == nil {
		return nil
	}
	segs := strings.Split(importSpec, "::")
	if len(segs) < 2 {
		return nil
	}
	// Identify the crate namespace prefix.
	head := segs[0]
	switch head {
	case "crate":
		segs = segs[1:]
	case "self":
		// `self::foo::Bar` → parent module's `foo` → sibling file.
		// Treat as relative to importing file's directory.
		return r.filesForRelativeImport(importingFile, segs[1:], 0)
	case "super":
		// `super::foo::Bar` — count leading `super` segments.
		levels := 0
		for levels < len(segs) && segs[levels] == "super" {
			levels++
		}
		return r.filesForRelativeImport(importingFile, segs[levels:], levels)
	default:
		if head != info.crateName {
			// External crate — out of scope for v1.
			return nil
		}
		segs = segs[1:]
	}
	// Drop the final ident (item name); resolve the rest as a path.
	if len(segs) < 1 {
		return nil
	}
	modSegs := segs[:len(segs)-1]
	// Empty modSegs means `use crate::Foo` — Foo is at crate root.
	if len(modSegs) == 0 {
		return r.rootFiles(info)
	}
	return r.fileForModulePath(info, modSegs)
}

// filesForRelativeImport resolves `self::foo::Bar` or
// `super::{super...}::foo::Bar`. levels is the number of super-s
// (0 for self). Returns the file(s) containing the module whose
// name is segs[len(segs)-2] relative to the importing file.
func (r *RustResolver) filesForRelativeImport(importingFile string, segs []string, levels int) []string {
	if len(segs) < 1 {
		return nil
	}
	info := r.crateInfoForFile(importingFile)
	if info == nil {
		return nil
	}
	base := filepath.Dir(importingFile)
	// Each `super` goes up one directory level.
	for i := 0; i < levels; i++ {
		base = filepath.Dir(base)
	}
	modSegs := segs[:len(segs)-1]
	if len(modSegs) == 0 {
		// self::Foo or super::Foo — the target is in the parent module
		// itself. Map `base` to a module file.
		rel, err := filepath.Rel(filepath.Join(info.root, "src"), base)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil
		}
		if rel == "." {
			return r.rootFiles(info)
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		return r.fileForModulePath(info, parts)
	}
	// Resolve modSegs relative to base.
	return r.resolveModuleDir(base, modSegs)
}

// fileForModulePath resolves a module path (split segments, no
// crate prefix) to the file(s) implementing that module under the
// crate's src/. Returns both mod.rs-style and flat-file-style
// candidates when both exist on disk.
func (r *RustResolver) fileForModulePath(info *rustCrateInfo, modSegs []string) []string {
	srcDir := filepath.Join(info.root, "src")
	return r.resolveModuleDir(srcDir, modSegs)
}

// resolveModuleDir walks modSegs under base, returning candidate
// file paths. `foo::bar` under base resolves to either
// base/foo/bar.rs or base/foo/bar/mod.rs; `foo` alone resolves to
// base/foo.rs or base/foo/mod.rs.
func (r *RustResolver) resolveModuleDir(base string, modSegs []string) []string {
	if len(modSegs) == 0 {
		return nil
	}
	// Walk all but last segment as directories.
	dir := base
	for i := 0; i < len(modSegs)-1; i++ {
		dir = filepath.Join(dir, modSegs[i])
	}
	last := modSegs[len(modSegs)-1]
	var out []string
	if fi, err := os.Stat(filepath.Join(dir, last+".rs")); err == nil && !fi.IsDir() {
		out = append(out, filepath.Join(dir, last+".rs"))
	}
	if fi, err := os.Stat(filepath.Join(dir, last, "mod.rs")); err == nil && !fi.IsDir() {
		out = append(out, filepath.Join(dir, last, "mod.rs"))
	}
	return out
}

// rootFiles returns the crate root file(s): src/lib.rs or src/main.rs.
func (r *RustResolver) rootFiles(info *rustCrateInfo) []string {
	var out []string
	for _, name := range []string{"lib.rs", "main.rs"} {
		p := filepath.Join(info.root, "src", name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// SamePackageFiles returns every .rs file under the same crate's src/
// as importingFile, excluding importingFile itself. Rust has no
// "same-package" concept; this is used by the rename pipeline as a
// candidate set for crate-wide cross-file search.
//
// The walk is bounded to src/ to avoid scanning target/ or tests/.
func (r *RustResolver) SamePackageFiles(importingFile string) []string {
	info := r.crateInfoForFile(importingFile)
	if info == nil {
		return nil
	}
	r.walkMu.Lock()
	files, ok := r.sameCrateMap[info.root]
	r.walkMu.Unlock()
	if !ok {
		files = r.walkCrateSrc(info)
		r.walkMu.Lock()
		r.sameCrateMap[info.root] = files
		r.walkMu.Unlock()
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		if f != importingFile {
			out = append(out, f)
		}
	}
	return out
}

func (r *RustResolver) walkCrateSrc(info *rustCrateInfo) []string {
	srcDir := filepath.Join(info.root, "src")
	var out []string
	filepath.Walk(srcDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".rs") {
			out = append(out, path)
		}
		return nil
	})
	return out
}

func (r *RustResolver) crateInfoForFile(file string) *rustCrateInfo {
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil
	}
	return r.canonCache.crateInfo(filepath.Dir(abs))
}
