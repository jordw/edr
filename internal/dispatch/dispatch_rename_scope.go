package dispatch

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	scopestore "github.com/jordw/edr/internal/scope/store"
)

// scopeAwareSameFileSpans computes rename spans via scope binding
// analysis. Returns (spans, true) on success; (nil, false) signals
// the caller to fall back to the regex-based path (unsupported
// language, parse failure, or decl not locatable).
//
// Binding-aware rename: a shadowed local with the same name in a
// nested scope will NOT be renamed, because its Binding.Decl points
// to the shadow, not the target.
func scopeAwareSameFileSpans(sym *index.SymbolInfo) ([]span, bool) {
	ext := strings.ToLower(filepath.Ext(sym.File))
	switch ext {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".py", ".pyi":
	default:
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
