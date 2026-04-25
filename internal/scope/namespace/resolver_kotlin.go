package namespace

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/kotlin"
)

// KotlinResolver mirrors JavaResolver but for Kotlin: package clause
// has no terminator, file extensions include .kt and .kts, otherwise
// the same FQN-import semantics. Top-level functions in Kotlin are
// rooted in the package directly (unlike Java which forces a class),
// so file-scope DeclIDs cover both top-level fn and top-level class.
type KotlinResolver struct {
	repoRoot   string
	parseMu    sync.Mutex
	parseCache map[string]*scope.Result
	srcCache   map[string][]byte
	pkgCache   map[string]string
}

func NewKotlinResolver(repoRoot string) *KotlinResolver {
	return &KotlinResolver{
		repoRoot:   repoRoot,
		parseCache: make(map[string]*scope.Result),
		srcCache:   make(map[string][]byte),
		pkgCache:   make(map[string]string),
	}
}

func (r *KotlinResolver) PackageOf(file string) string {
	r.parseMu.Lock()
	if pkg, ok := r.pkgCache[file]; ok {
		r.parseMu.Unlock()
		return pkg
	}
	r.parseMu.Unlock()
	src := r.Source(file)
	if src == nil {
		return ""
	}
	pkg := kotlinPackageClause(src)
	r.parseMu.Lock()
	r.pkgCache[file] = pkg
	r.parseMu.Unlock()
	return pkg
}

func (r *KotlinResolver) Source(file string) []byte {
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

func (r *KotlinResolver) Result(file string) *scope.Result {
	ext := strings.ToLower(filepath.Ext(file))
	if ext != ".kt" && ext != ".kts" {
		return nil
	}
	r.parseMu.Lock()
	if res, ok := r.parseCache[file]; ok {
		r.parseMu.Unlock()
		return res
	}
	r.parseMu.Unlock()

	src := r.Source(file)
	if src == nil {
		return nil
	}
	pkg := kotlinPackageClause(src)
	res := kotlin.ParseCanonical(file, pkg, src)

	r.parseMu.Lock()
	r.parseCache[file] = res
	r.pkgCache[file] = pkg
	r.parseMu.Unlock()
	return res
}

func (r *KotlinResolver) FilesForImport(importSpec, importingFile string) []string {
	if importSpec == "" {
		return nil
	}
	if strings.HasPrefix(importSpec, "kotlin.") || strings.HasPrefix(importSpec, "java.") || strings.HasPrefix(importSpec, "javax.") {
		return nil
	}
	if strings.HasSuffix(importSpec, ".*") {
		pkg := strings.TrimSuffix(importSpec, ".*")
		return r.findPackageFiles(pkg)
	}
	last := strings.LastIndex(importSpec, ".")
	if last < 0 {
		return nil
	}
	pkg := importSpec[:last]
	className := importSpec[last+1:]
	for _, f := range r.findPackageFiles(pkg) {
		base := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(f), ".kt"), ".kts")
		if base == className {
			return []string{f}
		}
	}
	// Kotlin top-level decls do not have to live in a file named
	// after them. If no filename match, return every .kt in the
	// package; the populator filters by name.
	return r.findPackageFiles(pkg)
}

func (r *KotlinResolver) findPackageFiles(pkg string) []string {
	pkgDir := strings.ReplaceAll(pkg, ".", string(filepath.Separator))
	var hits []string
	filepath.Walk(r.repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == ".edr" || name == "build" ||
				name == "target" || name == "out" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".kt" && ext != ".kts" {
			return nil
		}
		dir := filepath.Dir(path)
		if strings.HasSuffix(dir, string(filepath.Separator)+pkgDir) ||
			dir == filepath.Join(r.repoRoot, pkgDir) {
			hits = append(hits, path)
		}
		return nil
	})
	return hits
}

func (r *KotlinResolver) SamePackageFiles(importingFile string) []string {
	dir := filepath.Dir(importingFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	origPkg := r.PackageOf(importingFile)
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".kt" && ext != ".kts" {
			continue
		}
		sib := filepath.Join(dir, e.Name())
		if sib == importingFile {
			continue
		}
		if r.PackageOf(sib) != origPkg {
			continue
		}
		out = append(out, sib)
	}
	return out
}

// kotlinPackageClause scans src for the file's `package x.y` clause
// (no terminator) and returns the dotted name ("" if absent). Reuses
// javaPackageClause's byte-walking scanner — Kotlin's only meaningful
// difference is the optional `;` terminator, which the scanner
// already stops at.
func kotlinPackageClause(src []byte) string {
	return javaPackageClause(src)
}
