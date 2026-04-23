package namespace

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jordw/edr/internal/scope"
	scopecpp "github.com/jordw/edr/internal/scope/cpp"
)

var cppExtensions = []string{".cpp", ".cxx", ".cc", ".c++", ".hpp", ".hxx", ".hh", ".h++", ".h"}

type CppResolver struct {
	repoRoot   string
	parseMu    sync.Mutex
	parseCache map[string]*scope.Result
	srcCache   map[string][]byte
}

func NewCppResolver(repoRoot string) *CppResolver {
	return &CppResolver{
		repoRoot:   repoRoot,
		parseCache: make(map[string]*scope.Result),
		srcCache:   make(map[string][]byte),
	}
}

func (r *CppResolver) Source(file string) []byte {
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

func (r *CppResolver) CanonicalPath(file string) string {
	return cppCanonicalPathForFile(file, r.repoRoot)
}

func (r *CppResolver) Result(file string) *scope.Result {
	if !isCppFile(file) {
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
	res := scopecpp.ParseCanonical(file, canonical, src)
	r.parseMu.Lock()
	r.parseCache[file] = res
	r.srcCache[file] = src
	r.parseMu.Unlock()
	return res
}

func isCppFile(file string) bool {
	ext := strings.ToLower(filepath.Ext(file))
	for _, e := range cppExtensions {
		if ext == e {
			return true
		}
	}
	return false
}

// FilesForImport resolves a quoted `#include "path"`. Relative to
// the including file's directory first, then the repo root.
// Angle-bracket system headers are not resolved.
func (r *CppResolver) FilesForImport(importSpec, importingFile string) []string {
	if importSpec == "" || strings.HasPrefix(importSpec, "<") {
		return nil
	}
	candidates := []string{
		filepath.Join(filepath.Dir(importingFile), importSpec),
		filepath.Join(r.repoRoot, importSpec),
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil || seen[abs] {
			continue
		}
		seen[abs] = true
		if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
			out = append(out, abs)
		}
	}
	return out
}

// SamePackageFiles returns sibling source/header files in the same
// directory sharing the basename (foo.cpp ↔ foo.hpp, etc.).
func (r *CppResolver) SamePackageFiles(importingFile string) []string {
	stem := strings.TrimSuffix(filepath.Base(importingFile), filepath.Ext(importingFile))
	dir := filepath.Dir(importingFile)
	var out []string
	for _, ext := range cppExtensions {
		sib := filepath.Join(dir, stem+ext)
		if sib == importingFile {
			continue
		}
		if fi, err := os.Stat(sib); err == nil && !fi.IsDir() {
			out = append(out, sib)
		}
	}
	return out
}
