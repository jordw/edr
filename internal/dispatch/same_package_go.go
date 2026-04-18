package dispatch

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/golang"
)

// goIdentRef is a precise identifier reference in a sibling file,
// surfaced by scope parsing. Used by rename/changesig to rewrite
// exactly the identifier (not an enclosing symbol span) and by refs-to
// to list cross-file hits with per-hit line/column accuracy.
type goIdentRef struct {
	file      string // absolute path
	startByte uint32 // identifier start
	endByte   uint32 // identifier end
}

// goReadPackageClause extracts the package name from a Go source file.
// Returns "" if the clause is missing or malformed. Accepts the common
// leading-comment + package-clause layout; does NOT attempt to parse
// build tags or cgo.
func goReadPackageClause(src []byte) string {
	i := 0
	n := len(src)
	for i < n {
		// Skip whitespace.
		for i < n && (src[i] == ' ' || src[i] == '\t' || src[i] == '\r' || src[i] == '\n') {
			i++
		}
		if i >= n {
			return ""
		}
		// Skip line comment //...
		if i+1 < n && src[i] == '/' && src[i+1] == '/' {
			for i < n && src[i] != '\n' {
				i++
			}
			continue
		}
		// Skip block comment /* ... */
		if i+1 < n && src[i] == '/' && src[i+1] == '*' {
			i += 2
			for i+1 < n && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			if i+1 < n {
				i += 2
			}
			continue
		}
		break
	}
	// Expect "package"
	if i+7 > n || string(src[i:i+7]) != "package" {
		return ""
	}
	i += 7
	// Require at least one whitespace.
	if i >= n || !(src[i] == ' ' || src[i] == '\t') {
		return ""
	}
	for i < n && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	// Read identifier.
	start := i
	for i < n && (isGoIdentByte(src[i])) {
		i++
	}
	if i == start {
		return ""
	}
	return string(src[start:i])
}

func isGoIdentByte(b byte) bool {
	if b >= 'a' && b <= 'z' {
		return true
	}
	if b >= 'A' && b <= 'Z' {
		return true
	}
	if b >= '0' && b <= '9' {
		return true
	}
	return b == '_'
}

// goSamePackageRefs walks sibling .go files in origin's directory,
// verifies each has a matching `package X` clause, scope-parses, and
// returns precise identifier spans for refs to `name` that did NOT
// resolve locally (i.e. `BindUnresolved` — the file-scope-same-package
// case). Files whose refs all bind to local shadows are naturally
// excluded because those refs would be `BindResolved`, not unresolved.
//
// `originFile` is the file whose declaration we're finding refs for; it
// is always excluded from the walk. Test files (`_test.go`) are included
// iff their package clause matches `originPkg` (internal tests). Files
// with `package X_test` (external tests) are excluded because they'd
// import originPkg explicitly — those refs are cross-package.
func goSamePackageRefs(originFile, originPkg, name string) []goIdentRef {
	if originPkg == "" {
		return nil
	}
	dir := filepath.Dir(originFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []goIdentRef
	for _, e := range entries {
		fn := e.Name()
		if !strings.HasSuffix(fn, ".go") {
			continue
		}
		sibPath := filepath.Join(dir, fn)
		if sibPath == originFile {
			continue
		}
		sibSrc, err := os.ReadFile(sibPath)
		if err != nil {
			continue
		}
		if goReadPackageClause(sibSrc) != originPkg {
			continue
		}
		r := golang.Parse(sibPath, sibSrc)
		for _, ref := range r.Refs {
			if ref.Name != name {
				continue
			}
			if ref.Binding.Kind != scope.BindUnresolved {
				// Either resolved locally (shadow guard — skip) or a
				// property-access ref (handled by cross-package walk).
				continue
			}
			out = append(out, goIdentRef{
				file:      sibPath,
				startByte: ref.Span.StartByte,
				endByte:   ref.Span.EndByte,
			})
		}
	}
	return out
}

// goPackageOfFile reads originFile and returns its `package X` clause
// name. Returns "" when the file is missing or has no package clause.
func goPackageOfFile(originFile string) string {
	src, err := os.ReadFile(originFile)
	if err != nil {
		return ""
	}
	return goReadPackageClause(src)
}

// goSamePackageIdentSymbols returns synthetic SymbolInfo records —
// one per ref — with StartByte/EndByte set to the IDENTIFIER's byte
// range (not the enclosing symbol). Rename uses these: its regex
// rewrites inside each span, and a 7-byte identifier span with
// `\bCompute\b` matches only that exact identifier (never a shadowed
// local elsewhere in the file).
//
// Consumers that need the ENCLOSING SYMBOL span (e.g. changesig, which
// scans for `\bname\s*\(` across the symbol body) should use
// goSamePackageEnclosingCallers instead.
func goSamePackageIdentSymbols(originFile, originPkg, name string) []index.SymbolInfo {
	refs := goSamePackageRefs(originFile, originPkg, name)
	if len(refs) == 0 {
		return nil
	}
	out := make([]index.SymbolInfo, 0, len(refs))
	for _, r := range refs {
		out = append(out, index.SymbolInfo{
			Name:      name,
			File:      r.file,
			StartByte: r.startByte,
			EndByte:   r.endByte,
			Type:      "ref",
		})
	}
	return out
}

// goSamePackageEnclosingCallers returns the enclosing-symbol spans of
// caller symbols in sibling same-package files that have at least one
// unshadowed ref to `name`. One SymbolInfo per (file, enclosing symbol)
// pair. Used by changesig, which scans `\bname\s*\(` across each
// caller's body.
func goSamePackageEnclosingCallers(originFile, originPkg, name string, fileSymbols func(string) []index.SymbolInfo) []index.SymbolInfo {
	refs := goSamePackageRefs(originFile, originPkg, name)
	if len(refs) == 0 {
		return nil
	}
	// Group refs by file.
	byFile := map[string][]goIdentRef{}
	for _, r := range refs {
		byFile[r.file] = append(byFile[r.file], r)
	}
	var out []index.SymbolInfo
	type dedupKey struct {
		file  string
		start uint32
		end   uint32
	}
	seen := map[dedupKey]bool{}
	for file, fileRefs := range byFile {
		syms := fileSymbols(file)
		if len(syms) == 0 {
			continue
		}
		for _, r := range fileRefs {
			enclosing := findEnclosingSymbol(syms, r.startByte)
			if enclosing == nil {
				continue
			}
			key := dedupKey{file: file, start: enclosing.StartByte, end: enclosing.EndByte}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, *enclosing)
		}
	}
	return out
}

// findEnclosingSymbol returns the innermost symbol in `syms` whose
// byte range contains `pos`. Returns nil if no symbol contains it.
func findEnclosingSymbol(syms []index.SymbolInfo, pos uint32) *index.SymbolInfo {
	var best *index.SymbolInfo
	var bestSpan uint32 = ^uint32(0)
	for i := range syms {
		s := &syms[i]
		if s.StartByte <= pos && pos < s.EndByte {
			span := s.EndByte - s.StartByte
			if span < bestSpan {
				best = s
				bestSpan = span
			}
		}
	}
	return best
}
