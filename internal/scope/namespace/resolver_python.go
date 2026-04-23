package namespace

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jordw/edr/internal/scope"
	scopepy "github.com/jordw/edr/internal/scope/python"
)

// PythonResolver is a Resolver for Python codebases. Canonical paths
// are dotted module names rooted at the deepest ancestor directory
// that contains __init__.py — everything below that is part of the
// package. A repo with `foo/bar/baz.py` + `foo/__init__.py` +
// `foo/bar/__init__.py` gives `baz.py` canonical `foo.bar.baz`.
//
// v1 scope:
//   - Relative imports (`from .mod import X`, `from ..pkg.mod import X`).
//   - Absolute imports rooted at the repo (`from pkg.mod import X`
//     when `pkg/` is directly under the repo root OR visible in
//     any path where a resolvable candidate file exists).
//   - Bare module imports (`import foo.bar`) are NOT modeled — the
//     builder emits the imported name as a KindImport decl but its
//     signature encodes just the module path, which binds the
//     *module*, not any specific decl inside it.
type PythonResolver struct {
	repoRoot   string
	parseMu    sync.Mutex
	parseCache map[string]*scope.Result
	srcCache   map[string][]byte
}

func NewPythonResolver(repoRoot string) *PythonResolver {
	return &PythonResolver{
		repoRoot:   repoRoot,
		parseCache: make(map[string]*scope.Result),
		srcCache:   make(map[string][]byte),
	}
}

func (r *PythonResolver) Source(file string) []byte {
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

func (r *PythonResolver) CanonicalPath(file string) string {
	return pythonCanonicalPathForFile(file, r.repoRoot)
}

func (r *PythonResolver) Result(file string) *scope.Result {
	ext := strings.ToLower(filepath.Ext(file))
	if ext != ".py" && ext != ".pyi" {
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
	res := scopepy.ParseCanonical(file, canonical, src)

	r.parseMu.Lock()
	r.parseCache[file] = res
	r.srcCache[file] = src
	r.parseMu.Unlock()
	return res
}

// FilesForImport resolves a Python import spec to the module file.
// The spec may start with dots (relative) or be an absolute dotted
// path. For `from pkg.mod import X`, the resolver looks for
//
//	<root>/pkg/mod.py | .pyi
//	<root>/pkg/mod/__init__.py | .pyi
//
// where <root> is either the importing file's directory (walked up
// per leading-dot count for relative imports) or the repo root for
// absolute imports.
func (r *PythonResolver) FilesForImport(importSpec, importingFile string) []string {
	if importSpec == "" {
		return nil
	}
	dots := 0
	for dots < len(importSpec) && importSpec[dots] == '.' {
		dots++
	}
	tail := importSpec[dots:]
	var segments []string
	if tail != "" {
		segments = strings.Split(tail, ".")
	}

	var baseDir string
	if dots == 0 {
		// Absolute import — rooted at repoRoot.
		baseDir = r.repoRoot
	} else {
		// Relative — start at importer's directory, walk up dots-1.
		baseDir = filepath.Dir(importingFile)
		for step := 1; step < dots; step++ {
			baseDir = filepath.Dir(baseDir)
		}
	}

	joined := baseDir
	for _, seg := range segments {
		joined = filepath.Join(joined, seg)
	}

	var out []string
	for _, ext := range []string{".py", ".pyi"} {
		if joined != baseDir { // only check <joined>.py when we added segments
			cand := joined + ext
			if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
				out = append(out, cand)
				break
			}
		}
	}
	if len(out) == 0 {
		for _, ext := range []string{".py", ".pyi"} {
			cand := filepath.Join(joined, "__init__"+ext)
			if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
				out = append(out, cand)
				break
			}
		}
	}
	return out
}

// SamePackageFiles returns sibling .py files in the same directory
// (Python treats every module in a package as a peer accessible via
// `from . import sibling`). This is deliberately minimal — without
// it, cross-file refs in the same package would need every rename
// to go through an explicit `use`-style declaration, which isn't
// idiomatic Python.
func (r *PythonResolver) SamePackageFiles(importingFile string) []string {
	dir := filepath.Dir(importingFile)
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
		if !strings.HasSuffix(name, ".py") && !strings.HasSuffix(name, ".pyi") {
			continue
		}
		sib := filepath.Join(dir, name)
		if sib == importingFile {
			continue
		}
		out = append(out, sib)
	}
	return out
}
