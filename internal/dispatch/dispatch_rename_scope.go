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

// scopeSupported gates which languages use the scope-aware helpers
// for MUTATING commands (rename, changesig, extract). refs-to and the
// scope index itself handle more languages via scopestore.Parse, but
// mutations need the scope builder's binding resolution to be robust
// enough to not miss cross-file refs. Today that is only the mature
// three; the newer Java/Rust/Ruby/C/C++ builders serve refs-to well
// but leave enough false negatives that mutations should continue to
// use the regex + symbol-index fallback. Widen this gate per-language
// as each builder matures.
func scopeSupported(path string) bool {
	// Eval hook: forces the scope-aware or regex path for every language,
	// used by scripts/eval/rename_fp.sh to measure per-language over-rewrite
	// rate against the same corpus. Not intended for agent use — there is no
	// CLI flag. Presence of the env var is cheap to check per-call.
	switch os.Getenv("EDR_EVAL_FORCE_MODE") {
	case "scope":
		return true
	case "name-match":
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".py", ".pyi", ".java", ".kt", ".kts", ".rs", ".c", ".h":
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
	// Start from sym.StartByte (whole-symbol start) rather than
	// target.Span.StartByte (identifier), because expandToDocComment
	// looks for a newline immediately before its argument, and the
	// identifier is typically preceded by a keyword (`void`, `fn`) on
	// the same line, not by a newline.
	defStart := expandToDocComment(sym.File, sym.StartByte)
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

	// Namespace-driven cross-file resolution per language. Each
	// branch parses the target file with canonical DeclID hashing,
	// resolves the candidate files' imports + same-package siblings,
	// and emits refs that bind to the target by canonical DeclID.
	switch ext := strings.ToLower(filepath.Ext(sym.File)); ext {
	case ".go":
		crossSpans, ok := goCrossFileSpans(ctx, db, sym)
		if !ok {
			return nil, false
		}
		for f, spans := range crossSpans {
			out[f] = append(out[f], spans...)
		}
		return out, true
	case ".java":
		crossSpans, ok := javaCrossFileSpans(ctx, db, sym)
		if !ok {
			return nil, false
		}
		for f, spans := range crossSpans {
			out[f] = append(out[f], spans...)
		}
		return out, true
	case ".kt", ".kts":
		crossSpans, ok := kotlinCrossFileSpans(ctx, db, sym)
		if !ok {
			return nil, false
		}
		for f, spans := range crossSpans {
			out[f] = append(out[f], spans...)
		}
		return out, true
	case ".rs":
		// Only use the namespace-driven Rust path when we can
		// compute a canonical module path (Cargo.toml reachable).
		// Otherwise fall through to the generic ref-filtering path
		// below — which uses rustSameCrateRefs and preserves the
		// shadow guard for test-fixture setups with no Cargo.toml.
		if crossSpans, ok := rustCrossFileSpans(ctx, db, sym); ok {
			for f, spans := range crossSpans {
				out[f] = append(out[f], spans...)
			}
			return out, true
		}
	}

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

		// Property-access refs from the wrong target class are a common
		// false-positive source when renaming. Gate them: only include
		// `x.Name` hits when the target is actually a field/method
		// (something expected to be dotted into). For types, vars, etc.,
		// property access to a same-named member on an unrelated object
		// is NOT a rename target.
		//
		// Exception for Go: `pkg.Func()` (package-qualified call) is
		// emitted as a property_access ref by the scope builder. Without
		// this exception, cross-package Go renames silently drop all
		// call sites. Methods have sym.Receiver != "" (so sym.Type ==
		// "method") and hit the first branch; free functions have
		// sym.Type == "function" and the Go-specific branch lets them
		// through. The tradeoff: `obj.Func()` for an unrelated type
		// with a method named Func will also be rewritten. Accepted in
		// exchange for making cross-package rename work at all.
		// Go is handled by the namespace path above (early return).
		// Here we keep the original property_access gate for non-Go
		// languages: only methods/fields legitimately have `x.Name`
		// rewrites; types/functions/vars don't.
		propOK := sym.Type == "method" || sym.Type == "field"
		for _, ref := range result.Refs {
			if ref.Name != sym.Name {
				continue
			}
			if ref.Binding.Reason == "property_access" && !propOK {
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

		// C-family header/source split: the declaration lives in a .h
		// header and the definition in a .c/.cpp file. When the caller
		// of scopeAwareCrossFileSpans renames the `.c` definition, the
		// header file appears as a candidate but has no Refs for the
		// name — only a Decl. Emit its decl span so the header''s
		// declaration is rewritten too. This is idiomatic for C: a
		// declaration + definition pair represent the SAME logical
		// symbol (there is no separate DeclID). Restricted to same
		// language family to avoid false positives in non-C candidates.
		originIsC := isCFamily(strings.ToLower(filepath.Ext(sym.File)))
		candIsC := isCFamily(strings.ToLower(filepath.Ext(r.File)))
		if originIsC && candIsC {
			for i := range result.Decls {
				d := &result.Decls[i]
				if d.Name != sym.Name {
					continue
				}
				// Only file-scope decls — a local `int compute = 42` in
				// a different function is not our target.
				var scopeKind scope.ScopeKind
				if sid := int(d.Scope) - 1; sid >= 0 && sid < len(result.Scopes) {
					scopeKind = result.Scopes[sid].Kind
				}
				if scopeKind != scope.ScopeFile {
					continue
				}
				out[r.File] = append(out[r.File], span{
					start: d.Span.StartByte,
					end:   d.Span.EndByte,
					isDef: true,
				})
			}
		}
	}

	// Go, Java, and Kotlin are handled by the early-returning
	// namespace branches above.

	// Rust same-crate supplement: Rust has no package clause — modules
	// are defined by file layout (mod foo; → foo.rs). ParseRust does not
	// emit `mod` as an import and importReachesFile has no Rust case,
	// so FindSemanticReferences returns nothing cross-file for Rust.
	// Walk every .rs under the repo root, parse with scope, and emit
	// refs whose name matches. If the walker detects ambiguity (another
	// file defines a symbol with the same name), it returns ok=false —
	// we abort the whole scope-aware path and fall back to regex, since
	// a partial same-file-only scope result would silently miss legit
	// cross-file call sites.
	if strings.EqualFold(filepath.Ext(sym.File), ".rs") {
		refs, ok := rustSameCrateRefs(db.Root(), sym.File, sym.Name)
		if !ok {
			return nil, false
		}
		for _, idRef := range refs {
			out[idRef.file] = append(out[idRef.file], span{
				start: idRef.startByte,
				end:   idRef.endByte,
				isDef: false,
			})
		}
	}

	return out, true
}
