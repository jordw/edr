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

// scopeSupported gates which languages use scope-aware rename. refs-to and
// the scope index itself handle more languages via scopestore.Parse, but
// mutating rename needs binding resolution to be robust enough to avoid
// over-rewrites. Widen this gate per-language as each builder matures.
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
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts", ".py", ".pyi", ".java", ".kt", ".kts", ".rs",
		".c", ".h",
		".cpp", ".cxx", ".cc", ".c++", ".hpp", ".hxx", ".hh", ".h++",
		".rb",
		".cs",
		".swift",
		".php", ".phtml",
		".lua",
		".zig":
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
	// Select the target decl. Priority order:
	//   1. Identifier span exactly equals sym.StartByte (strict match).
	//   2. Identifier span is tightly contained in sym's range
	//      (sym.StartByte <= d.Span.StartByte && d.Span.EndByte <= sym.EndByte).
	//   3. d's FullSpan loosely contains sym.StartByte.
	// (2) outranks (3) so a builder bug that extends FullSpan past
	// the actual decl body can't win over a clean identifier match
	// — e.g. Swift's protocol method FullSpan bleeding into a
	// subsequent extension block containing an overriding method of
	// the same name.
	var tightTarget, looseTarget *scope.Decl
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
		if sym.StartByte <= d.Span.StartByte && d.Span.EndByte <= sym.EndByte {
			if tightTarget == nil {
				tightTarget = d
			}
			continue
		}
		if d.FullSpan.EndByte > 0 &&
			d.FullSpan.StartByte <= sym.StartByte && sym.StartByte < d.FullSpan.EndByte {
			if looseTarget == nil {
				looseTarget = d
			}
		}
	}
	if target == nil {
		if tightTarget != nil {
			target = tightTarget
		} else if looseTarget != nil {
			target = looseTarget
		}
	}
	if target == nil {
		// sym.File doesn't contain a decl matching sym.Name — this
		// happens for out-of-line definitions in C/C++ (the method
		// decl lives in a sibling header). Return (empty, true) so
		// the cross-file branch still runs and can find the real
		// decl in the header via its own sibling search.
		ext := strings.ToLower(filepath.Ext(sym.File))
		isCppish := ext == ".cpp" || ext == ".cxx" || ext == ".cc" || ext == ".c++" ||
			ext == ".hpp" || ext == ".hxx" || ext == ".hh" || ext == ".h++" ||
			ext == ".c" || ext == ".h"
		if isCppish {
			return nil, true
		}
		return nil, false
	}

	// Identifier span for the declaration itself.
	out := []span{{start: target.Span.StartByte, end: target.Span.EndByte}}

	// Doc-comment sweep: pick up `oldName` mentions in the leading
	// /// or // (or #) block so --comments=rewrite still rewrites
	// them. The apply layer treats each match as a separate span and
	// gates skip-mode via positionInComment.
	defStart := expandToDocComment(sym.File, sym.StartByte)
	if defStart < target.Span.StartByte {
		out = append(out, findIdentOccurrences(src, defStart, target.Span.StartByte, sym.Name)...)
	}

	for _, ref := range result.Refs {
		if ref.Binding.Decl == target.ID {
			out = append(out, span{start: ref.Span.StartByte, end: ref.Span.EndByte})
			continue
		}
		for _, cand := range ref.Binding.Candidates {
			if cand == target.ID {
				out = append(out, span{start: ref.Span.StartByte, end: ref.Span.EndByte})
				break
			}
		}
	}

	// Lua module-pattern (`function M.compute() end; M.compute()`) and
	// Zig namespaced calls (`pub fn compute(); ... pkg.compute()`) both
	// emit the call-site `compute` as a property_access ref whose
	// Binding.Decl is 0 — so the match-by-Decl loop above silently
	// drops it. Admit same-file property_access refs whose name
	// matches the target when the target is a function/method. Other
	// languages keep the strict by-Decl gate (Ruby/Java/C# already
	// handle their property forms in language-specific cross-file
	// branches with receiver-type filters).
	ext := strings.ToLower(filepath.Ext(sym.File))
	if (ext == ".lua" || ext == ".zig") &&
		(target.Kind == scope.KindFunction || target.Kind == scope.KindMethod) {
		for _, ref := range result.Refs {
			if ref.Name != sym.Name {
				continue
			}
			if ref.Binding.Reason != "property_access" {
				continue
			}
			out = append(out, span{start: ref.Span.StartByte, end: ref.Span.EndByte})
		}
	}
	return out, true
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

// isCFamily reports whether ext is a C-family extension whose declaration and
// definition may be split across header/source files.
func isCFamily(ext string) bool {
	switch ext {
	case ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hxx", ".hh":
		return true
	default:
		return false
	}
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
func scopeAwareCrossFileSpans(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) (map[string][]span, []string, bool) {
	if !scopeSupported(sym.File) {
		return nil, nil, false
	}

	// Origin file: reuse same-file resolution.
	originSpans, ok := scopeAwareSameFileSpans(sym)
	if !ok {
		return nil, nil, false
	}
	out := map[string][]span{sym.File: originSpans}

	// Namespace-driven cross-file resolution per language. Per-
	// extension renamers live in cross_file_registry.go and each
	// encodes its own commit policy (always-commit vs commit-only-
	// on-non-empty vs language-specific rules).
	if renamer, ok := crossFileRenamers[strings.ToLower(filepath.Ext(sym.File))]; ok {
		res := renamer(ctx, db, sym)
		if res.commit {
			for f, spans := range res.spans {
				out[f] = append(out[f], spans...)
			}
			return out, res.warnings, true
		}
		// Go/Java/Kotlin signal a hard failure (ok=false in their
		// inner resolver) by returning commit=false from a renamer
		// that has no fall-through path. Preserve the legacy hard-
		// abort for those — falling through to the generic ref
		// filter would mask a real resolver bug. Other languages
		// (Rust without Cargo, TS/JS / Python / C++ / C# / Swift /
		// PHP / Ruby with empty results) intentionally fall through.
		switch strings.ToLower(filepath.Ext(sym.File)) {
		case ".go", ".java", ".kt", ".kts":
			return nil, nil, false
		}
	}

	// Candidate files: symbol-index narrowed by import graph.
	refs, err := db.FindSemanticReferences(ctx, sym.Name, sym.File)
	if err != nil {
		return out, nil, true // origin still useful; cross-file narrowing failed
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
			return nil, nil, false
		}
		for _, idRef := range refs {
			out[idRef.file] = append(out[idRef.file], span{
				start: idRef.startByte,
				end:   idRef.endByte,
			})
		}
	}

	return out, nil, true
}
