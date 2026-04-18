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

// scopeAwareCrossFileSpans computes rename spans across the repo by
// narrowing candidate files via the symbol index and filtering each
// file's refs by scope binding. Cross-file DeclID matching is not
// possible (DeclID is file-local), so the filter instead EXCLUDES refs
// that bind to a local decl of the same name — those are shadows, not
// references to the target.
//
// Returns a map keyed by absolute file path; sym.File carries both the
// decl span and same-file refs from scopeAwareSameFileSpans. Returns
// (nil, false) on unsupported language, parse failure, or missing
// symbol-index narrowing data — the caller falls back to regex.
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
			// Shadow guard: skip refs bound to a local same-name decl.
			// DeclID is file-local, so it cannot equal our target's
			// ID — but if it points to a same-name local, that local is
			// shadowing the target in this file and we must not rewrite
			// it.
			if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl != 0 {
				if local, ok := declByID[ref.Binding.Decl]; ok && local.Name == sym.Name {
					continue
				}
			}
			out[r.File] = append(out[r.File], span{
				start: ref.Span.StartByte,
				end:   ref.Span.EndByte,
				isDef: false,
			})
		}
	}
	return out, true
}
