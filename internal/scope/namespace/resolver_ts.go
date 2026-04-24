package namespace

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jordw/edr/internal/scope"
	scopets "github.com/jordw/edr/internal/scope/ts"
)

// TSResolver is a Resolver for TypeScript/JavaScript codebases.
// File-scope decls hash with a canonical path (repo-root-relative,
// extension stripped) so `export function foo` in a/b.ts and
// `import { foo } from '../a/b'` in c.ts bind to the same DeclID.
//
// v1 scope:
//   - Relative imports (`./foo`, `../lib/util`) with the usual
//     extension fallbacks: .ts, .tsx, .d.ts, .js, .jsx, .mjs, .cjs,
//     and `<dir>/index.<ext>`.
//   - Barrel re-exports (`export { X } from './bar'`) are NOT
//     followed — the resolver treats the barrel file as the source
//     of X. Consumers that rename through re-exports will need a
//     reconcile pass later.
//
// Deferred:
//   - tsconfig paths (`@/components/...`) and baseUrl mappings.
//   - node_modules imports.
//   - CommonJS `require(...)`.
type TSResolver struct {
	repoRoot    string
	parseMu     sync.Mutex
	parseCache  map[string]*scope.Result
	srcCache    map[string][]byte
	tsconfigs   *tsConfigPaths
}

func NewTSResolver(repoRoot string) *TSResolver {
	return &TSResolver{
		repoRoot:   repoRoot,
		parseCache: make(map[string]*scope.Result),
		srcCache:   make(map[string][]byte),
		tsconfigs:  newTSConfigPaths(),
	}
}

func (r *TSResolver) Source(file string) []byte {
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

func (r *TSResolver) CanonicalPath(file string) string {
	return tsCanonicalPathForFile(file, r.repoRoot)
}

// tsFileExtensions lists the suffixes we'll try when resolving an
// import specifier that lacks an extension. Order matters: TypeScript
// is preferred over JS, and declaration files come last.
var tsFileExtensions = []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".d.ts"}

func (r *TSResolver) Result(file string) *scope.Result {
	if !isTSFile(file) {
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
	res := scopets.ParseCanonical(file, canonical, src)

	r.parseMu.Lock()
	r.parseCache[file] = res
	r.srcCache[file] = src
	r.parseMu.Unlock()
	return res
}

func isTSFile(file string) bool {
	for _, ext := range tsFileExtensions {
		if strings.HasSuffix(file, ext) {
			return true
		}
	}
	return false
}

// FilesForImport resolves a TS/JS import specifier to a file on
// disk. Only relative specifiers ('./', '../') are handled. The
// specifier may include an explicit extension; otherwise we try the
// standard fallbacks and `<dir>/index.<ext>`.
func (r *TSResolver) FilesForImport(importSpec, importingFile string) []string {
	if importSpec == "" {
		return nil
	}
	// Non-relative specs: try tsconfig.json paths mapping first.
	// If paths resolve to actual files, return those; otherwise
	// the spec is a bare package name (node_modules) and we skip.
	if !strings.HasPrefix(importSpec, "./") && !strings.HasPrefix(importSpec, "../") {
		cfg := r.tsconfigs.ConfigForFile(importingFile)
		if cfg != nil {
			for _, raw := range cfg.Resolve(importSpec) {
				if resolved := r.resolveTSCandidate(raw); resolved != "" {
					return []string{resolved}
				}
			}
		}
		return nil
	}
	base := filepath.Dir(importingFile)
	joined := filepath.Join(base, importSpec)
	// If specifier has an explicit supported extension, try as-is.
	for _, ext := range tsFileExtensions {
		if strings.HasSuffix(importSpec, ext) {
			if fi, err := os.Stat(joined); err == nil && !fi.IsDir() {
				return []string{joined}
			}
			return nil
		}
	}
	// Try <joined><ext> then <joined>/index<ext>.
	for _, ext := range tsFileExtensions {
		cand := joined + ext
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return []string{cand}
		}
	}
	for _, ext := range tsFileExtensions {
		cand := filepath.Join(joined, "index"+ext)
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return []string{cand}
		}
	}
	return nil
}

// SamePackageFiles returns nil. TS/JS has no "same package" concept
// outside of explicit imports — every cross-file binding goes
// through an import statement, which the populator handles.
func (r *TSResolver) SamePackageFiles(importingFile string) []string {
	return nil
}


// resolveTSCandidate tries the usual TS/JS extension-and-index
// fallbacks against a raw path (e.g. "/repo/src/components/Foo"),
// returning the first file that exists or "" when none do.
func (r *TSResolver) resolveTSCandidate(rawPath string) string {
	for _, ext := range tsFileExtensions {
		if strings.HasSuffix(rawPath, ext) {
			if fi, err := os.Stat(rawPath); err == nil && !fi.IsDir() {
				return rawPath
			}
			return ""
		}
	}
	for _, ext := range tsFileExtensions {
		cand := rawPath + ext
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}
	for _, ext := range tsFileExtensions {
		cand := filepath.Join(rawPath, "index"+ext)
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}
	return ""
}
