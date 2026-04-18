package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	scopestore "github.com/jordw/edr/internal/scope/store"
)

// scopeSupported reports whether the scope builders can parse path's
// language. The four query/mutation helpers share this gate.
func scopeSupported(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".py", ".pyi":
		return true
	}
	return false
}

// scopeAwareSameFileSpans computes rename spans via scope binding
// analysis. Returns (spans, true) on success; (nil, false) signals
// the caller to fall back to the regex-based path (unsupported
// language, parse failure, or decl not locatable).
//
// Binding-aware rename: a shadowed local with the same name in a
// nested scope will NOT be renamed, because its Binding.Decl points
// to the shadow, not the target.
func scopeAwareSameFileSpans(sym *index.SymbolInfo) ([]span, bool) {
	if !scopeSupported(sym.File) {
		return nil, false
	}
	src, err := os.ReadFile(sym.File)
	if err != nil {
		return nil, false
	}
	result := scopestore.Parse(sym.File, src)
	if result == nil {
		return nil, false
	}

	// Resolve the target decl by name. The symbol index reports a
	// range covering the full declaration (e.g., [func ... closing brace])
	// while scope records just the identifier position. Match if the
	// decl name matches AND the identifier span falls inside the
	// symbol-index range. Fall back to FullSpan containment for scope
	// builders that populate it.
	var target *scope.Decl
	for i := range result.Decls {
		d := &result.Decls[i]
		if d.Name != sym.Name {
			continue
		}
		if d.Span.StartByte == sym.StartByte {
			target = d
			break
		}
		if target == nil {
			if sym.StartByte <= d.Span.StartByte && d.Span.EndByte <= sym.EndByte {
				target = d
				continue
			}
			if d.FullSpan.EndByte > 0 &&
				d.FullSpan.StartByte <= sym.StartByte && sym.StartByte < d.FullSpan.EndByte {
				target = d
			}
		}
	}
	if target == nil {
		return nil, false
	}

	// Definition span: expand back to include the doc comment so that
	// --comments=rewrite picks up the leading /// or // documentation
	// block. End stays at the identifier so we do not rewrite mentions
	// inside the function body that scope did not bind to us.
	defStart := expandToDocComment(sym.File, target.Span.StartByte)
	out := []span{{start: defStart, end: target.Span.EndByte, isDef: true}}

	for _, ref := range result.Refs {
		if ref.Binding.Decl == target.ID {
			out = append(out, span{start: ref.Span.StartByte, end: ref.Span.EndByte, isDef: false})
			continue
		}
		for _, cand := range ref.Binding.Candidates {
			if cand == target.ID {
				out = append(out, span{start: ref.Span.StartByte, end: ref.Span.EndByte, isDef: false})
				break
			}
		}
	}
	return out, true
}

// scopeAwareRefSpansByFile returns binding-correct reference byte
// ranges grouped by file, excluding the target's declaration. It is
// the changesig-flavored counterpart of scopeAwareCrossFileSpans —
// changesig handles the definition edit separately, so it only needs
// refs. When crossFile is false, returns same-file refs only.
func scopeAwareRefSpansByFile(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo, crossFile bool) (map[string][]sigSpan, bool) {
	var raw map[string][]span
	if crossFile {
		m, ok := scopeAwareCrossFileSpans(ctx, db, sym)
		if !ok {
			return nil, false
		}
		raw = m
	} else {
		spans, ok := scopeAwareSameFileSpans(sym)
		if !ok {
			return nil, false
		}
		raw = map[string][]span{sym.File: spans}
	}
	out := make(map[string][]sigSpan, len(raw))
	for file, spans := range raw {
		src, err := os.ReadFile(file)
		for _, s := range spans {
			if s.isDef {
				continue // changesig handles the def edit itself
			}
			end := s.end
			if err == nil {
				end = widenToCallParen(src, s.end)
			}
			out[file] = append(out[file], sigSpan{start: s.start, end: end})
		}
	}
	return out, true
}

// widenToCallParen extends an identifier-span end to include the `(`
// that follows (allowing for whitespace and TS-style `<T>` generic
// params). Needed because changesig's regex `\bname\s*\(` must find
// the opening paren inside the span to identify a call site. Returns
// end unchanged if no call follows within a reasonable window.
func widenToCallParen(src []byte, end uint32) uint32 {
	const maxLookahead = 200
	limit := int(end) + maxLookahead
	if limit > len(src) {
		limit = len(src)
	}
	i := int(end)
	for i < limit && (src[i] == ' ' || src[i] == '\t' || src[i] == '\n' || src[i] == '\r') {
		i++
	}
	// Optional TS generic type arguments: skip balanced <...>.
	if i < limit && src[i] == '<' {
		depth := 1
		i++
		for i < limit && depth > 0 {
			switch src[i] {
			case '<':
				depth++
			case '>':
				depth--
			}
			i++
		}
		for i < limit && (src[i] == ' ' || src[i] == '\t') {
			i++
		}
	}
	if i < limit && src[i] == '(' {
		return uint32(i + 1)
	}
	return end
}

// filterCallersByScope drops SymbolInfo entries whose file/range
// does not actually contain a binding-correct reference to sym. It
// catches false positives returned by the caller search: a function
// whose body mentions the target's name only via a shadowed local
// variable, or inside a string/comment the symbol index misread.
// Returns callers unchanged if the language has no scope builder or
// parsing fails for every candidate.
func filterCallersByScope(callers []index.SymbolInfo, sym *index.SymbolInfo) []index.SymbolInfo {
	if !scopeSupported(sym.File) {
		return callers
	}
	parsed := make(map[string]*scope.Result)
	filtered := make([]index.SymbolInfo, 0, len(callers))
	for _, c := range callers {
		if !scopeSupported(c.File) {
			filtered = append(filtered, c)
			continue
		}
		result, ok := parsed[c.File]
		if !ok {
			src, err := os.ReadFile(c.File)
			if err == nil {
				result = scopestore.Parse(c.File, src)
			}
			parsed[c.File] = result
		}
		if result == nil {
			filtered = append(filtered, c)
			continue
		}
		declByID := make(map[scope.DeclID]*scope.Decl, len(result.Decls))
		for i := range result.Decls {
			declByID[result.Decls[i].ID] = &result.Decls[i]
		}
		keep := false
		for _, ref := range result.Refs {
			if ref.Name != sym.Name {
				continue
			}
			if ref.Span.StartByte < c.StartByte || ref.Span.EndByte > c.EndByte {
				continue
			}
			if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl != 0 {
				if local, ok := declByID[ref.Binding.Decl]; ok && local.Name == sym.Name {
					var localScopeKind scope.ScopeKind
					if sid := int(local.Scope) - 1; sid >= 0 && sid < len(result.Scopes) {
						localScopeKind = result.Scopes[sid].Kind
					}
					if local.Kind != scope.KindImport && localScopeKind != scope.ScopeFile {
						continue
					}
				}
			}
			keep = true
			break
		}
		if keep {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

// scopeAwareCrossFileSpans computes rename spans across the repo by
// narrowing candidate files via the symbol index and filtering each
// file's refs by scope binding. Cross-file DeclID matching is not
// possible (DeclID is file-local), so the filter instead EXCLUDES refs
// that bind to a local decl of the same name (nested-scope shadows).
// File-scope same-name decls and import decls pass through as they are
// typically the cross-file bindings we want to rewrite.
//
// Returns a map keyed by absolute file path. On unsupported language
// or parse failure, returns (nil, false) so the caller can fall back
// to regex.
func scopeAwareCrossFileSpans(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) (map[string][]span, bool) {
	if !scopeSupported(sym.File) {
		return nil, false
	}

	// Origin file: reuse same-file resolution.
	originSpans, ok := scopeAwareSameFileSpans(sym)
	if !ok {
		return nil, false
	}
	out := map[string][]span{sym.File: originSpans}

	// Candidate files: symbol-index narrowed by import graph.
	refs, err := db.FindSemanticReferences(ctx, sym.Name, sym.File)
	if err != nil {
		return out, true // origin still useful; cross-file narrowing failed
	}
	seen := map[string]bool{sym.File: true}
	for _, r := range refs {
		if seen[r.File] {
			continue
		}
		seen[r.File] = true
		if !scopeSupported(r.File) {
			continue
		}
		src, err := os.ReadFile(r.File)
		if err != nil {
			continue
		}
		result := scopestore.Parse(r.File, src)
		if result == nil {
			continue
		}

		// Index local decls by ID so we can identify same-name shadows.
		declByID := make(map[scope.DeclID]*scope.Decl, len(result.Decls))
		for i := range result.Decls {
			declByID[result.Decls[i].ID] = &result.Decls[i]
		}

		for _, ref := range result.Refs {
			if ref.Name != sym.Name {
				continue
			}
			// Shadow guard: skip refs bound to a local same-name decl,
			// but only when that decl is in a NESTED scope. File-scope
			// decls with the same name are typically cross-file bindings
			// — `import { X } from ...` (KindImport) for TS, or CJS
			// `const { X } = require(...)` (KindConst at file scope) for
			// JS. Those are the bindings we want to rewrite, not skip.
			// A same-name decl inside a function/block is a real shadow.
			if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl != 0 {
				if local, ok := declByID[ref.Binding.Decl]; ok && local.Name == sym.Name {
					var localScopeKind scope.ScopeKind
					if sid := int(local.Scope) - 1; sid >= 0 && sid < len(result.Scopes) {
						localScopeKind = result.Scopes[sid].Kind
					}
					if local.Kind != scope.KindImport && localScopeKind != scope.ScopeFile {
						continue
					}
				}
			}
			out[r.File] = append(out[r.File], span{
				start: ref.Span.StartByte,
				end:   ref.Span.EndByte,
				isDef: false,
			})
		}
	}

	// Go same-package supplement: FindSemanticReferences uses the import
	// graph to narrow candidates, and same-package files do not import
	// each other — so refs to the target from sibling files in the same
	// directory are MISSED. Walk the origin's package explicitly and add
	// every BindUnresolved ref whose name matches; refs that resolve to a
	// local decl are BindResolved and implicitly skipped (shadow guard).
	if strings.EqualFold(filepath.Ext(sym.File), ".go") {
		for _, idRef := range goSamePackageRefs(sym.File, goPackageOfFile(sym.File), sym.Name) {
			out[idRef.file] = append(out[idRef.file], span{
				start: idRef.startByte,
				end:   idRef.endByte,
				isDef: false,
			})
		}
	}

	return out, true
}
