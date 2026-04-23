package namespace

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jordw/edr/internal/scope"
	scopec "github.com/jordw/edr/internal/scope/c"
)

// CResolver is a Resolver for C codebases. The "namespace" for a
// translation unit is the set of decls visible through its
// `#include` directives plus its sibling files (same directory
// + basename — i.e. `foo.c`'s companion `foo.h` and vice-versa).
//
// v1 scope:
//   - #include "foo.h" (quoted) resolves relative to the including
//     file's directory, then to the repo root.
//   - #include <foo.h> (angle) is NOT resolved — system headers
//     live outside the repo and their contents aren't relevant
//     to cross-file rename.
//   - Sibling .c/.h pairs in the same directory join identities
//     via the canonical path convention (dir + basename). Split
//     include/src layouts do NOT merge.
type CResolver struct {
	repoRoot   string
	parseMu    sync.Mutex
	parseCache map[string]*scope.Result
	srcCache   map[string][]byte
}

func NewCResolver(repoRoot string) *CResolver {
	return &CResolver{
		repoRoot:   repoRoot,
		parseCache: make(map[string]*scope.Result),
		srcCache:   make(map[string][]byte),
	}
}

func (r *CResolver) Source(file string) []byte {
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

func (r *CResolver) CanonicalPath(file string) string {
	return cCanonicalPathForFile(file)
}

// Result parses a C/header file with its canonical path so exported
// file-scope decls hash consistently across .c/.h siblings.
func (r *CResolver) Result(file string) *scope.Result {
	ext := strings.ToLower(filepath.Ext(file))
	if ext != ".c" && ext != ".h" {
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
	res := scopec.ParseCanonical(file, canonical, src)
	r.parseMu.Lock()
	r.parseCache[file] = res
	r.srcCache[file] = src
	r.parseMu.Unlock()
	return res
}

// FilesForImport resolves a `#include "path"` spec to the file on
// disk. Search order: (1) relative to the including file's
// directory, (2) relative to the repo root. Angle-bracket system
// includes are not resolved.
func (r *CResolver) FilesForImport(importSpec, importingFile string) []string {
	// The C builder's KindImport signature format is
	// "<path>\x00<quoteStyle>" where quoteStyle is `"` or `<`. Only
	// the path portion reaches this function (the populator strips
	// the quote style). Still, defensively: skip anything with a
	// leading `<` to avoid treating system includes as local.
	path := importSpec
	if path == "" || strings.HasPrefix(path, "<") {
		return nil
	}
	candidates := []string{
		filepath.Join(filepath.Dir(importingFile), path),
		filepath.Join(r.repoRoot, path),
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
			out = append(out, abs)
		}
	}
	return out
}

// SamePackageFiles returns the sibling .c/.h file in the same
// directory with the same basename (foo.c → foo.h, foo.h → foo.c).
// C has no broader same-package concept; this is the minimum needed
// to let a rename propagate between a definition and its prototype.
func (r *CResolver) SamePackageFiles(importingFile string) []string {
	ext := strings.ToLower(filepath.Ext(importingFile))
	var other string
	switch ext {
	case ".c":
		other = ".h"
	case ".h":
		other = ".c"
	default:
		return nil
	}
	stem := strings.TrimSuffix(filepath.Base(importingFile), filepath.Ext(importingFile))
	sib := filepath.Join(filepath.Dir(importingFile), stem+other)
	if fi, err := os.Stat(sib); err == nil && !fi.IsDir() {
		return []string{sib}
	}
	return nil
}
