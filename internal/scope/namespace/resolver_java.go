package namespace

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/java"
)

// JavaResolver parses Java files using canonical paths derived from
// each files \`package x.y;\` clause. File-scope DeclIDs (top-level
// classes/interfaces/enums) are then identity-equal across every file
// in the same package, which lets the cross-file rename pass match
// an imported ref to the target by DeclID.
//
// Cached per-process. Construct one per command invocation.
type JavaResolver struct {
	repoRoot   string
	parseMu    sync.Mutex
	parseCache map[string]*scope.Result // absolute path → parsed
	srcCache   map[string][]byte        // absolute path → source bytes
	pkgCache   map[string]string        // absolute path → package clause
}

func NewJavaResolver(repoRoot string) *JavaResolver {
	return &JavaResolver{
		repoRoot:   repoRoot,
		parseCache: make(map[string]*scope.Result),
		srcCache:   make(map[string][]byte),
		pkgCache:   make(map[string]string),
	}
}

// PackageOf returns the package clause of a Java file, e.g.
// "org.springframework.core". Cached. "" when missing/malformed.
func (r *JavaResolver) PackageOf(file string) string {
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
	pkg := javaPackageClause(src)
	r.parseMu.Lock()
	r.pkgCache[file] = pkg
	r.parseMu.Unlock()
	return pkg
}

// Source returns cached source bytes for a file. Reads on miss.
func (r *JavaResolver) Source(file string) []byte {
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

// Result returns the parsed scope.Result for a Java file, using
// ParseCanonical so file-scope DeclIDs are canonical.
func (r *JavaResolver) Result(file string) *scope.Result {
	if !strings.EqualFold(filepath.Ext(file), ".java") {
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
	pkg := javaPackageClause(src)
	res := java.ParseCanonical(file, pkg, src)

	r.parseMu.Lock()
	r.parseCache[file] = res
	r.pkgCache[file] = pkg
	r.parseMu.Unlock()
	return res
}

// FilesForImport resolves a Java FQN import (e.g.
// "org.springframework.core.io.ClassPathResource") to the file that
// contains its top-level class. Returns at most one file path.
//
// Resolution rule: walks the repo for a .java file at <root>/<package
// path>/<className>.java. We pick the shortest matching path, since
// Java packages map to directories. Returns nil for stdlib imports
// (java.* / javax.*) — theyre effectively builtins.
func (r *JavaResolver) FilesForImport(importSpec, importingFile string) []string {
	if importSpec == "" {
		return nil
	}
	if strings.HasPrefix(importSpec, "java.") || strings.HasPrefix(importSpec, "javax.") {
		return nil
	}
	// Star imports (`import foo.bar.*`) bring in every class in the
	// package directory. Resolve to all .java files in that dir.
	if strings.HasSuffix(importSpec, ".*") {
		pkg := strings.TrimSuffix(importSpec, ".*")
		return r.findPackageFiles(pkg)
	}
	// Single-class import: foo.bar.ClassName → foo/bar/ClassName.java
	last := strings.LastIndex(importSpec, ".")
	if last < 0 {
		return nil
	}
	pkg := importSpec[:last]
	className := importSpec[last+1:]
	for _, f := range r.findPackageFiles(pkg) {
		base := strings.TrimSuffix(filepath.Base(f), ".java")
		if base == className {
			return []string{f}
		}
	}
	return nil
}

// findPackageFiles returns every .java file whose path ends with
// "<package as dir>/<...>.java" under the repo root. Cheap directory
// match — if the project layout follows convention, exactly one
// directory matches.
func (r *JavaResolver) findPackageFiles(pkg string) []string {
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
		if !strings.HasSuffix(path, ".java") {
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

// SamePackageFiles returns the .java files in importingFiles
// directory that share its package clause. Java requires same-package
// files to live in the same directory, so this is just a directory
// scan + clause check.
func (r *JavaResolver) SamePackageFiles(importingFile string) []string {
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
		if !strings.HasSuffix(e.Name(), ".java") {
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

// javaPackageClause scans src for the file's `package x.y.z;` clause
// and returns the dotted name ("" if absent). Walks bytes manually
// rather than using a regex so it stays consistent with the lexer-
// based scope builder; tolerates leading line comments, block
// comments, and Javadoc.
func javaPackageClause(src []byte) string {
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			if i+1 < len(src) {
				i += 2
			}
		case c == '@':
			// Annotation on the package clause (rare). Skip the ident
			// and any `(...)` argument list.
			i++
			for i < len(src) && isJavaPkgIdentByte(src[i]) {
				i++
			}
			if i < len(src) && src[i] == '(' {
				depth := 1
				i++
				for i < len(src) && depth > 0 {
					switch src[i] {
					case '(':
						depth++
					case ')':
						depth--
					}
					i++
				}
			}
		default:
			if c == 'p' && i+7 <= len(src) && string(src[i:i+7]) == "package" &&
				(i+7 == len(src) || !isJavaPkgIdentByte(src[i+7])) {
				j := i + 7
				for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
					j++
				}
				start := j
				for j < len(src) && (isJavaPkgIdentByte(src[j]) || src[j] == '.') {
					j++
				}
				return string(src[start:j])
			}
			return ""
		}
	}
	return ""
}

func isJavaPkgIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_'
}
