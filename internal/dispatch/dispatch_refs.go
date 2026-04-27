package dispatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/scope"
	scopec "github.com/jordw/edr/internal/scope/c"
	scopecpp "github.com/jordw/edr/internal/scope/cpp"
	"github.com/jordw/edr/internal/scope/csharp"
	scopelua "github.com/jordw/edr/internal/scope/lua"
	"github.com/jordw/edr/internal/scope/golang"
	"github.com/jordw/edr/internal/scope/java"
	"github.com/jordw/edr/internal/scope/kotlin"
	"github.com/jordw/edr/internal/scope/php"
	"github.com/jordw/edr/internal/scope/python"
	"github.com/jordw/edr/internal/scope/ruby"
	"github.com/jordw/edr/internal/scope/rust"
	scopestore "github.com/jordw/edr/internal/scope/store"
	"github.com/jordw/edr/internal/scope/swift"
	"github.com/jordw/edr/internal/scope/ts"
	scopezig "github.com/jordw/edr/internal/scope/zig"
)

// runRefsTo implements `edr refs-to file:Symbol`. Parses the file with
// the appropriate language scope builder, locates the symbol, and lists
// every reference bound to it. Single-file for v1; cross-file resolution
// requires the persistent scope index that isn't wired yet.
func runRefsTo(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("refs-to: need exactly one argument of the form file:Symbol")
	}
	arg := args[0]
	colon := strings.LastIndex(arg, ":")
	if colon <= 0 || colon == len(arg)-1 {
		return nil, fmt.Errorf("refs-to: argument %q must be file:Symbol", arg)
	}
	relFile := arg[:colon]
	symbolName := arg[colon+1:]

	absFile := relFile
	if !filepath.IsAbs(absFile) {
		absFile = filepath.Join(root, relFile)
	}
	src, err := os.ReadFile(absFile)
	if err != nil {
		return nil, fmt.Errorf("refs-to: read %s: %w", relFile, err)
	}

	ext := strings.ToLower(filepath.Ext(absFile))

	// Prefer the persisted scope index when it's present and fresh.
	// Falls through to on-demand parse on miss/stale.
	var result *scope.Result
	var persistedIndex *scopestore.Index
	if idx, _ := scopestore.Load(db.EdrDir()); idx != nil {
		persistedIndex = idx
		if r := idx.ResultFor(root, relFile); r != nil {
			result = r
		}
	}
	if result == nil {
		// Delegate to the shared scope.Parse dispatcher so any language
		// registered there (ts/js/jsx, go, python, java, rust, ruby, c,
		// c++) is handled.
		result = scopestore.Parse(relFile, src)
		if result == nil {
			return nil, fmt.Errorf("refs-to: unsupported language %q", ext)
		}
	}
	_ = persistedIndex // reserved for future cross-file resolution via DeclID

	// Find the decl. Prefer file-scope match; fall back to any decl with the name.
	decl := scope.FindDeclByName(result, symbolName)
	if decl == nil {
		for i := range result.Decls {
			if result.Decls[i].Name == symbolName {
				decl = &result.Decls[i]
				break
			}
		}
	}
	if decl == nil {
		return nil, fmt.Errorf("refs-to: symbol %q not declared in %s", symbolName, relFile)
	}

	refs := scope.RefsToDecl(result, decl.ID)

	// Build per-file entries: every ref gets { file, line, col, reason }.
	lines := computeLineStarts(src)
	var entries []refEntry
	for _, ref := range refs {
		line, col := byteToLineCol(lines, ref.Span.StartByte)
		entries = append(entries, refEntry{
			file:   output.Rel(absFile),
			line:   line,
			col:    col,
			span:   [2]uint32{ref.Span.StartByte, ref.Span.EndByte},
			reason: ref.Binding.Reason,
			kind:   ref.Binding.Kind,
		})
	}

	// Property-access refs (obj.name): when the target is a method, field,
	// or function likely accessed via property (common for package-qualified
	// Go calls `pkg.Foo()` and class methods), add probable matches by name.
	// These are name-only — they match any same-named method on any object —
	// so they're tagged as probable. Skip if the target is clearly not an
	// access target (e.g., a local var).
	if propertyAccessLikelyTarget(decl) {
		for _, ref := range result.Refs {
			if ref.Name != symbolName {
				continue
			}
			if ref.Binding.Reason != "property_access" {
				continue
			}
			line, col := byteToLineCol(lines, ref.Span.StartByte)
			entries = append(entries, refEntry{
				file:   output.Rel(absFile),
				line:   line,
				col:    col,
				span:   [2]uint32{ref.Span.StartByte, ref.Span.EndByte},
				reason: "property_access",
				kind:   scope.BindProbable,
			})
		}
	}

	// Cross-file: admit decls in scopes that are cross-file visible —
	// file scope plus class/interface/namespace, so methods on classes
	// in Ruby/Java/Python/etc. are reachable. Excludes function/block/for
	// scopes, where decls are local and have no cross-file callers.
	declScopeKind := scope.ScopeFile
	if sid := int(decl.Scope) - 1; sid >= 0 && sid < len(result.Scopes) {
		declScopeKind = result.Scopes[sid].Kind
	}
	crossFileVisible := declScopeKind == scope.ScopeFile ||
		declScopeKind == scope.ScopeClass ||
		declScopeKind == scope.ScopeInterface ||
		declScopeKind == scope.ScopeNamespace
	if crossFileVisible {
		switch ext {
		case ".go":
			entries = append(entries, goCrossFileRefs(absFile, symbolName, persistedIndex, root)...)
			if len(symbolName) > 0 && symbolName[0] >= 'A' && symbolName[0] <= 'Z' {
				entries = append(entries, goCrossPackagePropRefs(root, absFile, symbolName, persistedIndex)...)
			}
		case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts":
			entries = append(entries, tsCrossFileRefs(root, absFile, symbolName, persistedIndex)...)
		case ".py", ".pyi":
			entries = append(entries, pyCrossFileRefs(root, absFile, symbolName, persistedIndex)...)
		case ".java":
			entries = append(entries, javaCrossFileRefs(root, absFile, symbolName, persistedIndex)...)
		case ".rb":
			entries = append(entries, rubyCrossFileRefs(root, absFile, symbolName, persistedIndex)...)
		case ".rs":
			entries = append(entries, rustCrossFileRefs(root, absFile, symbolName, persistedIndex)...)
		case ".kt", ".kts":
			entries = append(entries, kotlinCrossFileRefs(root, absFile, symbolName, persistedIndex)...)
		case ".swift":
			entries = append(entries, swiftCrossFileRefs(root, absFile, symbolName, persistedIndex)...)
		case ".php":
			entries = append(entries, phpCrossFileRefs(root, absFile, symbolName, persistedIndex)...)
		case ".c", ".h":
			entries = append(entries, cCrossFileRefs(root, absFile, symbolName, persistedIndex)...)
		case ".cpp", ".cxx", ".cc", ".c++", ".hpp", ".hxx", ".hh", ".h++":
			entries = append(entries, cppCrossFileRefs(root, absFile, symbolName, persistedIndex)...)
		case ".lua":
			entries = append(entries, luaCrossFileRefs(root, absFile, symbolName, persistedIndex)...)
		case ".zig":
			entries = append(entries, zigCrossFileRefs(root, absFile, symbolName, persistedIndex)...)
		case ".cs":
			entries = append(entries, csharpCrossFileRefs(root, absFile, symbolName, persistedIndex)...)
		}
	}

	// Snapshot covered (file, startByte) before strict filtering — the
	// name-match-only diff below should subtract everything scope bound,
	// not just what strict kept.
	covered := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		covered[nameMatchKey(e.file, e.span[0])] = struct{}{}
	}

	// --strict: filter to BindResolved refs only. Suppresses probable/
	// ambiguous/unresolved entries entirely so callers that need a high-
	// confidence ref set don't have to post-filter.
	strict := flagBool(flags, "strict", false)
	if strict {
		filtered := entries[:0]
		for _, e := range entries {
			if e.kind == scope.BindResolved {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	budget := flagInt(flags, "budget", 0)
	truncated := false
	if budget > 0 && len(entries) > budget {
		entries = entries[:budget]
		truncated = true
	}

	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		m := map[string]any{
			"file":   e.file,
			"line":   e.line,
			"col":    e.col,
			"span":   e.span,
			"reason": e.reason,
		}
		if e.kind != scope.BindResolved {
			m["binding"] = bindingKindName(e.kind)
		}
		out = append(out, m)
	}

	// Binding quality: count entries by BindingKind so consumers can see
	// at a glance how many refs scope could pin down vs leave probable or
	// unresolved. Surfaces the moat (extractive scope precision) without
	// the user needing to scan per-ref reasons.
	var nResolved, nProbable, nAmbiguous, nUnresolved int
	for _, e := range entries {
		switch e.kind {
		case scope.BindResolved:
			nResolved++
		case scope.BindProbable:
			nProbable++
		case scope.BindAmbiguous:
			nAmbiguous++
		case scope.BindUnresolved:
			nUnresolved++
		}
	}
	binding := map[string]any{}
	if nResolved > 0 {
		binding["resolved"] = nResolved
	}
	if nProbable > 0 {
		binding["probable"] = nProbable
	}
	if nAmbiguous > 0 {
		binding["ambiguous"] = nAmbiguous
	}
	if nUnresolved > 0 {
		binding["unresolved"] = nUnresolved
	}

	declLine, declCol := byteToLineCol(lines, decl.Span.StartByte)
	payload := map[string]any{
		"file": output.Rel(absFile),
		"decl": map[string]any{
			"name": decl.Name,
			"kind": string(decl.Kind),
			"line": declLine,
			"col":  declCol,
		},
		"count":     len(entries),
		"refs":      out,
		"truncated": truncated,
	}
	if len(binding) > 0 {
		payload["binding"] = binding
	}

	// --include-name-match: report word-bounded text matches that scope
	// did not bind. Default form is count-only; --by-file aggregates by
	// file, --list emits per-occurrence entries (capped by --budget).
	if flagBool(flags, "include_name_match", false) {
		nm, nmErr := db.FindNameMatchReferences(ctx, symbolName)
		if nmErr == nil {
			byFile := flagBool(flags, "by_file", false)
			listAll := flagBool(flags, "list", false)
			extra, perFile, sample := computeNameMatchOnly(nm, covered, root, byFile || listAll, listAll, budget)
			payload["name_match_extra"] = extra
			if byFile && len(perFile) > 0 {
				payload["name_match_by_file"] = perFile
			}
			if listAll && len(sample) > 0 {
				payload["name_match_entries"] = sample
			}
		}
	}

	// Stale-index warning: refs-to may undercount cross-file refs when
	// the trigram + symbol index hasn't been ticked since recent edits.
	// Surface it so callers don't trust an incomplete count silently.
	if idx.IsStale(root, db.EdrDir()) {
		payload["stale_index"] = true
		payload["warnings"] = []any{"index is stale — cross-file refs may be undercounted; run 'edr index' to refresh"}
	}
	return payload, nil
}

// nameMatchKey forms the dedup key for a name-match diff. Files use
// the relative path so absolute-path entries from FindNameMatchReferences
// align with the relative paths we've already emitted in entries.
func nameMatchKey(relFile string, startByte uint32) string {
	return fmt.Sprintf("%s:%d", relFile, startByte)
}

// computeNameMatchOnly subtracts the covered set from the name-match
// candidates and returns (totalCount, perFileCounts, sampleEntries).
// perFileCounts is populated only when byFile is true; sampleEntries
// only when wantSample is true (and capped by budget).
func computeNameMatchOnly(matches []index.SymbolInfo, covered map[string]struct{}, root string, byFile, wantSample bool, budget int) (int, map[string]int, []map[string]any) {
	total := 0
	var perFile map[string]int
	if byFile {
		perFile = map[string]int{}
	}
	var sample []map[string]any
	cap := budget
	if cap <= 0 {
		cap = 50
	}
	for _, m := range matches {
		rel := output.Rel(m.File)
		if _, ok := covered[nameMatchKey(rel, m.StartByte)]; ok {
			continue
		}
		total++
		if byFile {
			perFile[rel]++
		}
		if wantSample && len(sample) < cap {
			sample = append(sample, map[string]any{
				"file": rel,
				"line": int(m.StartLine),
				"span": [2]uint32{m.StartByte, m.EndByte},
			})
		}
	}
	return total, perFile, sample
}

// goCrossFileRefs returns refs to `name` from sibling .go files in
// the same package (same directory). Uses the persisted scope index
// when available to skip re-parsing; falls back to filesystem walk.
func goCrossFileRefs(originFile, name string, idx *scopestore.Index, root string) []refEntry {
	dir := filepath.Dir(originFile)
	originPkg := goPackageOfFile(originFile)
	var out []refEntry
	iter := func(sibPath string, r *scope.Result, sibSrc []byte) {
		if sibPath == originFile {
			return
		}
		if filepath.Dir(sibPath) != dir {
			return
		}
		if !strings.HasSuffix(sibPath, ".go") {
			return
		}
		// Verify the sibling actually shares the origin's `package X`
		// clause. Files with a different clause (e.g. `package bar`
		// next to `package foo`, or `package foo_test` vs `package foo`)
		// are NOT same-package. Internal test files (`_test.go` with
		// matching clause) are included — their refs-to call sites
		// should be found.
		if originPkg != "" && goReadPackageClause(sibSrc) != originPkg {
			return
		}
		sibLines := computeLineStarts(sibSrc)
		for _, ref := range r.Refs {
			if ref.Name != name {
				continue
			}
			// Classify each ref:
			//   - unresolved bare ident in a same-package file → this
			//     IS the cross-file binding to our target. Mark as
			//     BindResolved (target name is visible across all files
			//     in the package; Go forbids duplicate top-level names
			//     within a package, so the match is unambiguous).
			//   - property_access `x.name` → cross-package access via a
			//     package-qualified call. Keep as probable.
			//   - otherwise (locally resolved shadow, builtin, etc.) →
			//     skip.
			reason := ""
			kind := scope.BindProbable
			if ref.Binding.Kind == scope.BindUnresolved {
				reason = "cross_file_same_package"
				kind = scope.BindResolved
			} else if ref.Binding.Reason == "property_access" {
				reason = "property_access"
			} else {
				continue
			}
			line, col := byteToLineCol(sibLines, ref.Span.StartByte)
			out = append(out, refEntry{
				file:   output.Rel(sibPath),
				line:   line,
				col:    col,
				span:   [2]uint32{ref.Span.StartByte, ref.Span.EndByte},
				reason: reason,
				kind:   kind,
			})
		}
	}
	if idx != nil {
		for rel, r := range idx.AllResults() {
			abs := filepath.Join(root, rel)
			src, err := os.ReadFile(abs)
			if err != nil {
				continue
			}
			iter(abs, r, src)
		}
		return out
	}
	// Fallback: filesystem walk of the origin's directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		sibPath := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(sibPath)
		if err != nil {
			continue
		}
		r := golang.Parse(sibPath, src)
		iter(sibPath, r, src)
	}
	return out
}

type refEntry struct {
	file   string
	line   int
	col    int
	span   [2]uint32
	reason string
	kind   scope.BindingKind
}

// goCrossPackagePropRefs walks all .go files under root (outside the
// origin's package directory) and collects property-access refs whose
// name matches the target. These are the `pkg.Name` call sites for
// exported cross-package Go symbols — imprecise (we don't verify the
// package alias actually points at origin's package), but high-signal
// in practice for capitalized names.
func goCrossPackagePropRefs(root, originFile, name string, idx *scopestore.Index) []refEntry {
	originDir := filepath.Dir(originFile)
	var out []refEntry
	collect := func(path string, r *scope.Result, src []byte) {
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return
		}
		if filepath.Dir(path) == originDir {
			return // same package; covered by goCrossFileRefs
		}
		sibLines := computeLineStarts(src)
		for _, ref := range r.Refs {
			if ref.Name != name || ref.Binding.Reason != "property_access" {
				continue
			}
			line, col := byteToLineCol(sibLines, ref.Span.StartByte)
			out = append(out, refEntry{
				file:   output.Rel(path),
				line:   line,
				col:    col,
				span:   [2]uint32{ref.Span.StartByte, ref.Span.EndByte},
				reason: "cross_package_property",
				kind:   scope.BindProbable,
			})
		}
	}
	if idx != nil {
		for rel, r := range idx.AllResults() {
			abs := filepath.Join(root, rel)
			src, err := os.ReadFile(abs)
			if err != nil {
				continue
			}
			collect(abs, r, src)
		}
		return out
	}
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			n := info.Name()
			if n == ".git" || n == ".edr" || n == "vendor" || n == "node_modules" || n == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		r := golang.Parse(path, src)
		collect(path, r, src)
		return nil
	})
	return out
}

// pyCrossFileRefs walks .py files under root and collects refs to
// `name` that are unresolved (likely cross-module imports not followed)
// or property-access probable (`pkg.name` call sites). Heuristic but
// high-signal — Python import resolution (dotted paths, from..import,
// relative imports) is deferred.
func pyCrossFileRefs(root, originFile, name string, idx *scopestore.Index) []refEntry {
	var out []refEntry
	collect := func(path string, r *scope.Result, src []byte) {
		if path == originFile || !strings.HasSuffix(path, ".py") {
			return
		}
		// Only scan files that mention the name at all.
		seen := false
		for _, ref := range r.Refs {
			if ref.Name == name {
				seen = true
				break
			}
		}
		if !seen {
			return
		}
		sibLines := computeLineStarts(src)
		for _, ref := range r.Refs {
			if ref.Name != name {
				continue
			}
			var reason string
			switch {
			case ref.Binding.Reason == "property_access":
				reason = "property_access"
			case ref.Binding.Kind == scope.BindResolved && ref.Binding.Reason == "builtin":
				continue
			case ref.Binding.Kind == scope.BindResolved:
				reason = "cross_file_import"
			case ref.Binding.Kind == scope.BindUnresolved:
				reason = "cross_file_unresolved"
			default:
				continue
			}
			line, col := byteToLineCol(sibLines, ref.Span.StartByte)
			out = append(out, refEntry{
				file:   output.Rel(path),
				line:   line,
				col:    col,
				span:   [2]uint32{ref.Span.StartByte, ref.Span.EndByte},
				reason: reason,
				kind:   scope.BindProbable,
			})
		}
	}
	if idx != nil {
		for rel, r := range idx.AllResults() {
			abs := filepath.Join(root, rel)
			src, err := os.ReadFile(abs)
			if err != nil {
				continue
			}
			collect(abs, r, src)
		}
		return out
	}
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			n := info.Name()
			if n == ".git" || n == ".edr" || n == "__pycache__" || n == ".venv" || n == "node_modules" || n == "build" || n == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if path == originFile || !strings.HasSuffix(path, ".py") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		r := python.Parse(path, src)
		// Only scan files that mention the name at all.
		seen := false
		for _, ref := range r.Refs {
			if ref.Name == name {
				seen = true
				break
			}
		}
		if !seen {
			return nil
		}
		sibLines := computeLineStarts(src)
		for _, ref := range r.Refs {
			if ref.Name != name {
				continue
			}
			var reason string
			switch {
			case ref.Binding.Reason == "property_access":
				reason = "property_access"
			case ref.Binding.Kind == scope.BindResolved && ref.Binding.Reason == "builtin":
				continue
			case ref.Binding.Kind == scope.BindResolved:
				reason = "cross_file_import"
			case ref.Binding.Kind == scope.BindUnresolved:
				reason = "cross_file_unresolved"
			default:
				continue
			}
			line, col := byteToLineCol(sibLines, ref.Span.StartByte)
			out = append(out, refEntry{
				file:   output.Rel(path),
				line:   line,
				col:    col,
				span:   [2]uint32{ref.Span.StartByte, ref.Span.EndByte},
				reason: reason,
				kind:   scope.BindProbable,
			})
		}
		return nil
	})
	return out
}

// tsCrossFileRefs walks TS-like files under root and collects refs
// whose name matches `name` and whose binding is either unresolved or
// resolves to a local Import decl with the same name. Heuristic in
// the absence of full import-path resolution; false positives are
// possible when unrelated files happen to use the same identifier as
// an imported name.
func tsCrossFileRefs(root, originFile, name string, idx *scopestore.Index) []refEntry {
	var out []refEntry
	collect := func(path string, r *scope.Result, src []byte) {
		if path == originFile {
			return
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts":
		default:
			return
		}
		if strings.HasSuffix(path, ".d.ts") {
			return
		}
		hasImport := false
		hasPropertyAccess := false
		for _, d := range r.Decls {
			if d.Name == name && d.Kind == scope.KindImport {
				hasImport = true
				break
			}
		}
		if !hasImport {
			for _, ref := range r.Refs {
				if ref.Name == name && ref.Binding.Reason == "property_access" {
					hasPropertyAccess = true
					break
				}
			}
		}
		if !hasImport && !hasPropertyAccess {
			return
		}
		sibLines := computeLineStarts(src)
		for _, ref := range r.Refs {
			if ref.Name != name {
				continue
			}
			var reason string
			switch {
			case ref.Binding.Reason == "property_access":
				reason = "property_access"
			case ref.Binding.Kind == scope.BindResolved && ref.Binding.Reason == "builtin":
				continue
			case ref.Binding.Kind == scope.BindResolved:
				reason = "cross_file_import"
			case ref.Binding.Kind == scope.BindUnresolved:
				if !hasPropertyAccess {
					continue
				}
				reason = "cross_file_import"
			default:
				reason = "cross_file_import"
			}
			line, col := byteToLineCol(sibLines, ref.Span.StartByte)
			out = append(out, refEntry{
				file:   output.Rel(path),
				line:   line,
				col:    col,
				span:   [2]uint32{ref.Span.StartByte, ref.Span.EndByte},
				reason: reason,
				kind:   scope.BindProbable,
			})
		}
	}
	if idx != nil {
		for rel, r := range idx.AllResults() {
			abs := filepath.Join(root, rel)
			src, err := os.ReadFile(abs)
			if err != nil {
				continue
			}
			collect(abs, r, src)
		}
		return out
	}
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			n := info.Name()
			// Skip heavy / irrelevant directories.
			if n == "node_modules" || n == ".git" || n == ".edr" || n == "dist" || n == "build" || n == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if path == originFile {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts":
		default:
			return nil
		}
		if strings.HasSuffix(path, ".d.ts") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		r := ts.Parse(path, src)
		// Does this file reference `name` at all? Check for an Import
		// decl with the name (named import) or any ref (property access
		// like `foo.Name` where foo is imported).
		hasImport := false
		hasPropertyAccess := false
		for _, d := range r.Decls {
			if d.Name == name && d.Kind == scope.KindImport {
				hasImport = true
				break
			}
		}
		if !hasImport {
			for _, ref := range r.Refs {
				if ref.Name == name && ref.Binding.Reason == "property_access" {
					hasPropertyAccess = true
					break
				}
			}
		}
		if !hasImport && !hasPropertyAccess {
			return nil
		}
		sibLines := computeLineStarts(src)
		for _, ref := range r.Refs {
			if ref.Name != name {
				continue
			}
			var reason string
			switch {
			case ref.Binding.Reason == "property_access":
				reason = "property_access"
			case ref.Binding.Kind == scope.BindResolved && ref.Binding.Reason == "builtin":
				continue // builtin, not our symbol
			case ref.Binding.Kind == scope.BindResolved:
				// Resolved to the local Import decl — this is our target.
				reason = "cross_file_import"
			case ref.Binding.Kind == scope.BindUnresolved:
				// File doesn't import the name — only accept if we saw a
				// property-access match above (possible re-export context).
				if !hasPropertyAccess {
					continue
				}
				reason = "cross_file_import"
			default:
				reason = "cross_file_import"
			}
			line, col := byteToLineCol(sibLines, ref.Span.StartByte)
			out = append(out, refEntry{
				file:   output.Rel(path),
				line:   line,
				col:    col,
				span:   [2]uint32{ref.Span.StartByte, ref.Span.EndByte},
				reason: reason,
				kind:   scope.BindProbable,
			})
		}
		return nil
	})
	return out
}

// genericCrossFileRefs is the shared implementation used by java/ruby/rust
// cross-file resolution. Mirrors pyCrossFileRefs: for each non-origin file
// whose extension is in `exts`, collect refs named `name` and tag them as
// probable with a cross_file_* reason derived from the source binding.
// Builtin-resolved refs are skipped (not our symbol). Uses the persisted
// scope index when available; falls back to an on-demand filesystem walk.
func genericCrossFileRefs(
	root, originFile, name string,
	idx *scopestore.Index,
	exts map[string]bool,
	skipDirs map[string]bool,
	parseFn func(path string, src []byte) *scope.Result,
) []refEntry {
	var out []refEntry
	collect := func(path string, r *scope.Result, src []byte) {
		if path == originFile {
			return
		}
		if !exts[strings.ToLower(filepath.Ext(path))] {
			return
		}
		seen := false
		for _, ref := range r.Refs {
			if ref.Name == name {
				seen = true
				break
			}
		}
		if !seen {
			return
		}
		sibLines := computeLineStarts(src)
		for _, ref := range r.Refs {
			if ref.Name != name {
				continue
			}
			var reason string
			switch {
			case ref.Binding.Reason == "property_access":
				reason = "property_access"
			case ref.Binding.Kind == scope.BindResolved && ref.Binding.Reason == "builtin":
				continue
			case ref.Binding.Kind == scope.BindResolved:
				reason = "cross_file_import"
			case ref.Binding.Kind == scope.BindUnresolved:
				reason = "cross_file_unresolved"
			default:
				continue
			}
			line, col := byteToLineCol(sibLines, ref.Span.StartByte)
			out = append(out, refEntry{
				file:   output.Rel(path),
				line:   line,
				col:    col,
				span:   [2]uint32{ref.Span.StartByte, ref.Span.EndByte},
				reason: reason,
				kind:   scope.BindProbable,
			})
		}
	}
	if idx != nil {
		for rel, r := range idx.AllResults() {
			abs := filepath.Join(root, rel)
			src, err := os.ReadFile(abs)
			if err != nil {
				continue
			}
			collect(abs, r, src)
		}
		return out
	}
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if path == originFile || !exts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		collect(path, parseFn(path, src), src)
		return nil
	})
	return out
}

// javaCrossFileRefs: same-project .java files that reference `name`.
// Heuristic (no import-path resolution); false positives possible when
// unrelated files happen to use the same simple class/method name.
func javaCrossFileRefs(root, originFile, name string, idx *scopestore.Index) []refEntry {
	return genericCrossFileRefs(root, originFile, name, idx,
		map[string]bool{".java": true},
		map[string]bool{".git": true, ".edr": true, "target": true, "build": true, ".gradle": true, "out": true, "node_modules": true},
		java.Parse)
}

// rubyCrossFileRefs: same-project .rb files that reference `name`.
// Ruby's dynamic dispatch means unresolved bare idents are plausible
// method calls on any receiver; this walk surfaces same-name call sites
// across the repo.
func rubyCrossFileRefs(root, originFile, name string, idx *scopestore.Index) []refEntry {
	return genericCrossFileRefs(root, originFile, name, idx,
		map[string]bool{".rb": true},
		map[string]bool{".git": true, ".edr": true, "vendor": true, ".bundle": true, "coverage": true, "tmp": true, "log": true, "node_modules": true},
		ruby.Parse)
}

// rustCrossFileRefs: same-project .rs files that reference `name`.
// Rust `use` paths are not followed; false positives possible for
// same-name types across unrelated modules.
func rustCrossFileRefs(root, originFile, name string, idx *scopestore.Index) []refEntry {
	return genericCrossFileRefs(root, originFile, name, idx,
		map[string]bool{".rs": true},
		map[string]bool{".git": true, ".edr": true, "target": true, "node_modules": true},
		rust.Parse)
}

// kotlinCrossFileRefs: same-project .kt/.kts files that reference
// `name`. Heuristic (no import resolution); false positives possible
// when unrelated files use the same simple name.
func kotlinCrossFileRefs(root, originFile, name string, idx *scopestore.Index) []refEntry {
	return genericCrossFileRefs(root, originFile, name, idx,
		map[string]bool{".kt": true, ".kts": true},
		map[string]bool{".git": true, ".edr": true, "build": true, ".gradle": true, "out": true, "node_modules": true},
		kotlin.Parse)
}

// swiftCrossFileRefs: same-project .swift files that reference `name`.
// Swift `import` statements are not followed.
func swiftCrossFileRefs(root, originFile, name string, idx *scopestore.Index) []refEntry {
	return genericCrossFileRefs(root, originFile, name, idx,
		map[string]bool{".swift": true},
		map[string]bool{".git": true, ".edr": true, ".build": true, "Pods": true, "DerivedData": true, "build": true, "node_modules": true},
		swift.Parse)
}

// phpCrossFileRefs: same-project .php files that reference `name`.
// PHP `use` / `namespace` paths are not followed — false positives
// possible for unqualified names across namespaces.
func phpCrossFileRefs(root, originFile, name string, idx *scopestore.Index) []refEntry {
	return genericCrossFileRefs(root, originFile, name, idx,
		map[string]bool{".php": true},
		map[string]bool{".git": true, ".edr": true, "vendor": true, "node_modules": true, "cache": true},
		php.Parse)
}

// cCrossFileRefs: same-project .c/.h files that reference `name`.
// #include directives are not followed; cross-file refs are name-only.
func cCrossFileRefs(root, originFile, name string, idx *scopestore.Index) []refEntry {
	return genericCrossFileRefs(root, originFile, name, idx,
		map[string]bool{".c": true, ".h": true},
		map[string]bool{".git": true, ".edr": true, "build": true, "dist": true, "out": true, "node_modules": true},
		scopec.Parse)
}

// cppCrossFileRefs walks every .cpp/.hpp (and friends) under root,
// parses with the C++ scope builder, and emits refs whose name
// matches. Mirrors cCrossFileRefs — class methods declared in a
// header have callers spread across the .cpp/.hpp pair plus the
// translation units that include the header. Without this branch,
// refs-to from a `.hpp` decl returned count=0 even when the
// canonical-merge had wired the def + decl into the same DeclID.
func cppCrossFileRefs(root, originFile, name string, idx *scopestore.Index) []refEntry {
	return genericCrossFileRefs(root, originFile, name, idx,
		map[string]bool{".cpp": true, ".cxx": true, ".cc": true, ".c++": true, ".hpp": true, ".hxx": true, ".hh": true, ".h++": true},
		map[string]bool{".git": true, ".edr": true, "build": true, "dist": true, "out": true, "node_modules": true},
		scopecpp.Parse)
}

// luaCrossFileRefs walks every .lua file under root for refs to name.
// Lua does not statically track imports (`require` / `dofile` are
// runtime calls), so the walk is unconditional — analogous to
// rubyCrossFileRefs.
func luaCrossFileRefs(root, originFile, name string, idx *scopestore.Index) []refEntry {
	return genericCrossFileRefs(root, originFile, name, idx,
		map[string]bool{".lua": true},
		map[string]bool{".git": true, ".edr": true, "node_modules": true, ".luarocks": true},
		scopelua.Parse)
}

// zigCrossFileRefs walks every .zig file under root. Zig does have
// `@import("foo.zig")` but the import graph isn't modeled in the
// trigram-driven FindSemanticReferences pipeline; walking all .zig
// files matches what the user expects from refs-to and surfaces
// the property-access call sites (`lib.fn`) the resolver emits.
func zigCrossFileRefs(root, originFile, name string, idx *scopestore.Index) []refEntry {
	return genericCrossFileRefs(root, originFile, name, idx,
		map[string]bool{".zig": true},
		map[string]bool{".git": true, ".edr": true, "zig-cache": true, "zig-out": true, ".zig-cache": true},
		scopezig.Parse)
}

// csharpCrossFileRefs: same-project .cs files that reference `name`.
// `using` directives are not followed; `partial class` sites across
// files are picked up both as refs and (via store reconciliation) as
// merged DeclIDs.
func csharpCrossFileRefs(root, originFile, name string, idx *scopestore.Index) []refEntry {
	return genericCrossFileRefs(root, originFile, name, idx,
		map[string]bool{".cs": true},
		map[string]bool{".git": true, ".edr": true, "bin": true, "obj": true, "packages": true, "node_modules": true},
		csharp.Parse)
}

// computeLineStarts returns the byte offset of the start of each line.
// lines[0] = 0 (start of file).
func computeLineStarts(src []byte) []uint32 {
	starts := []uint32{0}
	for i, c := range src {
		if c == '\n' {
			starts = append(starts, uint32(i+1))
		}
	}
	return starts
}

// byteToLineCol returns 1-based line and column for a byte offset.
func byteToLineCol(lineStarts []uint32, b uint32) (line, col int) {
	// Binary search: find largest i such that lineStarts[i] <= b
	lo, hi := 0, len(lineStarts)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if lineStarts[mid] <= b {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1, int(b-lineStarts[lo]) + 1
}

// propertyAccessLikelyTarget reports whether a decl might be referenced
// via `obj.name` somewhere. Returns true for things that can legitimately
// be on the RHS of a dot access: methods, fields, top-level functions
// (cross-package), classes (for static members), consts/types exported
// from modules, and import aliases (Go `pkg.Foo`). Returns false for
// purely local decls like params and local vars.
func propertyAccessLikelyTarget(d *scope.Decl) bool {
	switch d.Kind {
	case scope.KindMethod, scope.KindField:
		return true
	case scope.KindFunction, scope.KindClass, scope.KindInterface,
		scope.KindType, scope.KindConst, scope.KindImport,
		scope.KindEnum, scope.KindNamespace:
		return true
	}
	return false
}

func bindingKindName(k scope.BindingKind) string {
	switch k {
	case scope.BindResolved:
		return "resolved"
	case scope.BindAmbiguous:
		return "ambiguous"
	case scope.BindProbable:
		return "probable"
	case scope.BindUnresolved:
		return "unresolved"
	}
	return "unknown"
}
