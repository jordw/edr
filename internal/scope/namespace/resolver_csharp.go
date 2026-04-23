package namespace

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jordw/edr/internal/scope"
	scopecsharp "github.com/jordw/edr/internal/scope/csharp"
)

// CSharpResolver is a Resolver for C# codebases. v1 only
// populates same-directory siblings — namespace-clause-based
// cross-directory resolution (e.g., C# `using Foo.Bar;`, PHP
// `use Foo\Bar\Baz;`) is deferred. The dispatch branch falls
// through to the generic path when namespace matching yields no
// cross-file hits.
type CSharpResolver struct {
	repoRoot   string
	parseMu    sync.Mutex
	parseCache map[string]*scope.Result
	srcCache   map[string][]byte
}

func NewCSharpResolver(repoRoot string) *CSharpResolver {
	return &CSharpResolver{
		repoRoot:   repoRoot,
		parseCache: make(map[string]*scope.Result),
		srcCache:   make(map[string][]byte),
	}
}

func (r *CSharpResolver) Source(file string) []byte {
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

func (r *CSharpResolver) CanonicalPath(file string) string {
	return csharpCanonicalPathForFile(file, r.repoRoot)
}

func (r *CSharpResolver) Result(file string) *scope.Result {
	if !strings.EqualFold(filepath.Ext(file), ".cs") {
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
	res := scopecsharp.ParseCanonical(file, canonical, src)
	r.parseMu.Lock()
	r.parseCache[file] = res
	r.srcCache[file] = src
	r.parseMu.Unlock()
	return res
}

// FilesForImport returns nil — namespace-qualified import
// resolution is deferred to a future phase that parses namespace
// clauses out of source.
func (r *CSharpResolver) FilesForImport(importSpec, importingFile string) []string {
	return nil
}

// SamePackageFiles returns siblings in the same directory.
func (r *CSharpResolver) SamePackageFiles(importingFile string) []string {
	dir := filepath.Dir(importingFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	exts := []string{".cs"}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		matched := false
		for _, want := range exts {
			if ext == want {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		sib := filepath.Join(dir, e.Name())
		if sib == importingFile {
			continue
		}
		out = append(out, sib)
	}
	return out
}
