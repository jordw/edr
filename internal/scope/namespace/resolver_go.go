package namespace

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/golang"
)

// GoResolver is a Resolver for repositories rooted at a Go module.
// It parses Go files using canonical paths (module path + relative
// directory) so file-scope DeclIDs are identity-equal across files in
// the same package.
//
// Parses and canonical-path lookups are cached per-process. The
// resolver is the single entry point that the Go populator uses to
// look up imports. Callers construct one per command invocation; the
// cache is not reused across commands to avoid stale reads after an
// edit.
type GoResolver struct {
	repoRoot   string
	canonCache *goCanonicalPathCache
	parseMu    sync.Mutex
	parseCache map[string]*scope.Result // absolute path → parsed
	srcCache   map[string][]byte        // absolute path → source bytes
}

// NewGoResolver constructs a resolver rooted at repoRoot. The
// module-path cache will find go.mod starting from each file's dir.
func NewGoResolver(repoRoot string) *GoResolver {
	return &GoResolver{
		repoRoot:   repoRoot,
		canonCache: newGoCanonicalPathCache(),
		parseCache: make(map[string]*scope.Result),
		srcCache:   make(map[string][]byte),
	}
}

// Source returns the cached source bytes for a file, reading from
// disk on miss. Used by the cross-file rename pass to disambiguate
// property_access refs (which need the byte preceding the ref span).
// Reading via the resolver shares the cache with Result so a file
// parsed by Result keeps its source available without re-reading.
func (r *GoResolver) Source(file string) []byte {
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

// CanonicalPath returns the canonical path for a Go file. Exported
// so the caller can compute the targets canonical path (to hash
// its DeclID consistently with what the resolver will produce for
// sibling files).
func (r *GoResolver) CanonicalPath(file string) string {
	return r.canonCache.CanonicalPathForGoFile(file)
}

// Result returns the parsed scope.Result for a Go file, using
// ParseCanonical so the file-scope DeclIDs are canonical.
func (r *GoResolver) Result(file string) *scope.Result {
	if strings.ToLower(filepath.Ext(file)) != ".go" {
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
	res := golang.ParseCanonical(file, canonical, src)

	r.parseMu.Lock()
	r.parseCache[file] = res
	r.srcCache[file] = src
	r.parseMu.Unlock()
	return res
}

// FilesForImport resolves a Go import path to the files in the
// imported packages directory. The import path is a module-relative
// path (e.g., "github.com/jordw/edr/internal/output"); the file set
// is every .go in the matching directory.
//
// Stdlib imports (no dot in first segment) resolve to no files —
// theyre effectively builtins for our purposes.
func (r *GoResolver) FilesForImport(importSpec, importingFile string) []string {
	mod := r.moduleInfoForFile(importingFile)
	if mod == nil {
		return nil
	}
	// Stdlib packages have no dot in the first segment.
	if !strings.Contains(strings.SplitN(importSpec, "/", 2)[0], ".") {
		return nil
	}
	// Module-local imports only: the import path must begin with the
	// module path.
	if !strings.HasPrefix(importSpec, mod.modulePath) {
		return nil
	}
	rel := strings.TrimPrefix(importSpec, mod.modulePath)
	rel = strings.TrimPrefix(rel, "/")
	dir := filepath.Join(mod.root, filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	return out
}

// SamePackageFiles returns the .go files in importingFiles directory
// that share its package clause. Used by the populator to add
// SourceSamePackage entries.
func (r *GoResolver) SamePackageFiles(importingFile string) []string {
	dir := filepath.Dir(importingFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	origSrc, err := os.ReadFile(importingFile)
	if err != nil {
		return nil
	}
	origPkg := golang.PackageClause(origSrc)
	if origPkg == "" {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		sib := filepath.Join(dir, name)
		if sib == importingFile {
			continue
		}
		src, err := os.ReadFile(sib)
		if err != nil {
			continue
		}
		if golang.PackageClause(src) != origPkg {
			continue
		}
		out = append(out, sib)
	}
	return out
}

func (r *GoResolver) moduleInfoForFile(file string) *goModInfo {
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil
	}
	return r.canonCache.moduleInfo(filepath.Dir(abs))
}

