package namespace

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jordw/edr/internal/scope"
	scopephp "github.com/jordw/edr/internal/scope/php"
)

// PHPResolver is a Resolver for PHP codebases. v1 only
// populates same-directory siblings — namespace-clause-based
// cross-directory resolution (e.g., C# `using Foo.Bar;`, PHP
// `use Foo\Bar\Baz;`) is deferred. The dispatch branch falls
// through to the generic path when namespace matching yields no
// cross-file hits.
type PHPResolver struct {
	repoRoot   string
	parseMu    sync.Mutex
	parseCache map[string]*scope.Result
	srcCache   map[string][]byte
}

func NewPHPResolver(repoRoot string) *PHPResolver {
	return &PHPResolver{
		repoRoot:   repoRoot,
		parseCache: make(map[string]*scope.Result),
		srcCache:   make(map[string][]byte),
	}
}

func (r *PHPResolver) Source(file string) []byte {
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

func (r *PHPResolver) CanonicalPath(file string) string {
	return phpCanonicalPathForFile(file, r.repoRoot)
}

func (r *PHPResolver) Result(file string) *scope.Result {
	if !isPhpFile(file) {
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
	res := scopephp.ParseCanonical(file, canonical, src)
	r.parseMu.Lock()
	r.parseCache[file] = res
	r.srcCache[file] = src
	r.parseMu.Unlock()
	return res
}

// FilesForImport returns nil — namespace-qualified import
// resolution is deferred to a future phase that parses namespace
// clauses out of source.
func (r *PHPResolver) FilesForImport(importSpec, importingFile string) []string {
	return nil
}

// SamePackageFiles returns siblings in the same directory.
func (r *PHPResolver) SamePackageFiles(importingFile string) []string {
	dir := filepath.Dir(importingFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	exts := []string{".php", ".phtml"}
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

func isPhpFile(file string) bool {
	ext := strings.ToLower(filepath.Ext(file))
	return ext == ".php" || ext == ".phtml"
}
