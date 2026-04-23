package namespace

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jordw/edr/internal/scope"
	scoperuby "github.com/jordw/edr/internal/scope/ruby"
)

// RubyResolver is a Resolver for Ruby codebases. Canonical paths
// are repo-relative with the .rb extension stripped. Imports are
// resolved for `require_relative "path"` only; $LOAD_PATH-based
// `require` is not modeled (it depends on runtime state).
type RubyResolver struct {
	repoRoot   string
	parseMu    sync.Mutex
	parseCache map[string]*scope.Result
	srcCache   map[string][]byte
}

func NewRubyResolver(repoRoot string) *RubyResolver {
	return &RubyResolver{
		repoRoot:   repoRoot,
		parseCache: make(map[string]*scope.Result),
		srcCache:   make(map[string][]byte),
	}
}

func (r *RubyResolver) Source(file string) []byte {
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

func (r *RubyResolver) CanonicalPath(file string) string {
	return rubyCanonicalPathForFile(file, r.repoRoot)
}

func (r *RubyResolver) Result(file string) *scope.Result {
	if !strings.HasSuffix(strings.ToLower(file), ".rb") {
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
	res := scoperuby.ParseCanonical(file, canonical, src)
	r.parseMu.Lock()
	r.parseCache[file] = res
	r.srcCache[file] = src
	r.parseMu.Unlock()
	return res
}

// FilesForImport resolves `require_relative "spec"` to a file.
// `require "spec"` is NOT handled — it depends on $LOAD_PATH.
func (r *RubyResolver) FilesForImport(importSpec, importingFile string) []string {
	if importSpec == "" {
		return nil
	}
	base := filepath.Dir(importingFile)
	cand := filepath.Join(base, importSpec)
	if !strings.HasSuffix(cand, ".rb") {
		cand += ".rb"
	}
	if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
		return []string{cand}
	}
	return nil
}

// SamePackageFiles returns sibling .rb files in the same directory.
// Ruby has no strict module-file mapping; same-dir files often
// `require_relative` each other, and for rename purposes treating
// them as peers is a useful approximation.
func (r *RubyResolver) SamePackageFiles(importingFile string) []string {
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
		if !strings.HasSuffix(e.Name(), ".rb") {
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
