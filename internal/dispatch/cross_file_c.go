package dispatch

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/namespace"
)

// cCrossFileSpans is the C branch of scopeAwareCrossFileSpans. For
// a file-scope exported decl, it finds the companion prototype in
// the sibling .h (identical DeclID via canonical-path hashing) and
// every caller reached through a quoted `#include` of that header.
//
// v1 scope:
//   - Sibling .c/.h pairs in the same directory.
//   - Callers that `#include "..."` the target's header directly.
//   - Static decls do not propagate (translation-unit-local).
//
// Deferred:
//   - Transitive #include walks (only first-level includes are
//     populated).
//   - Separately-located header/source layouts (`include/foo.h` +
//     `src/foo.c`) — different canonical paths, DeclIDs don't merge.
//   - Angle-bracket system headers (not relevant for project refs).
//
// Returns (spans, ok). ok=false only when the target is not a .c/.h
// file or its parse fails — caller then falls back.
func cCrossFileSpans(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) (map[string][]span, bool) {
	ext := strings.ToLower(filepath.Ext(sym.File))
	if ext != ".c" && ext != ".h" {
		return nil, false
	}
	out := map[string][]span{}
	resolver := namespace.NewCResolver(db.Root())
	targetRes := resolver.Result(sym.File)
	if targetRes == nil {
		return nil, false
	}

	// Find the target Decl.
	var target *scope.Decl
	for i := range targetRes.Decls {
		d := &targetRes.Decls[i]
		if d.Name != sym.Name {
			continue
		}
		if d.Span.StartByte >= sym.StartByte && d.Span.EndByte <= sym.EndByte {
			target = d
			break
		}
	}
	if target == nil {
		return out, true
	}

	// Candidate files: #include-graph neighbors plus the sibling
	// .c/.h file. The index returns every file that mentions the
	// name textually; we additionally force the sibling because a
	// prototype in the .h shares the target's DeclID but may not be
	// flagged by the index.
	candidates := map[string]bool{}
	if refs, err := db.FindSemanticReferences(ctx, sym.Name, sym.File); err == nil {
		for _, r := range refs {
			if r.File != sym.File {
				candidates[r.File] = true
			}
		}
	}
	for _, sib := range resolver.SamePackageFiles(sym.File) {
		candidates[sib] = true
	}

	pop := namespace.CPopulator(resolver)
	for cand := range candidates {
		candRes := resolver.Result(cand)
		if candRes == nil {
			continue
		}
		ns := namespace.Build(cand, candRes, resolver, pop)
		if !ns.Matches(sym.Name, target.ID) {
			continue
		}
		declByID := make(map[scope.DeclID]*scope.Decl, len(candRes.Decls))
		for i := range candRes.Decls {
			declByID[candRes.Decls[i].ID] = &candRes.Decls[i]
		}

		// File-scope decls whose DeclID matches the target. This
		// catches the prototype in the sibling header (which shares
		// the target's DeclID via canonical-path hashing).
		for _, d := range candRes.Decls {
			if d.ID != target.ID {
				continue
			}
			if d.Kind == scope.KindImport {
				continue
			}
			out[cand] = append(out[cand], span{
				start: d.Span.StartByte,
				end:   d.Span.EndByte,
				isDef: true,
			})
		}

		// Refs. Shadow guard skips refs bound to a nested-scope local
		// with the same name. File-scope same-name decls that are
		// NOT the target (e.g., a static helper with the same name)
		// shadow the include; skip refs resolving to them.
		for _, ref := range candRes.Refs {
			if ref.Name != sym.Name {
				continue
			}
			if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl != 0 {
				if local, ok := declByID[ref.Binding.Decl]; ok && local.Name == sym.Name {
					if local.ID != target.ID {
						continue
					}
				}
			}
			// Refs through `obj.name` in C are struct field accesses,
			// not function calls on our target. Skip property-access
			// refs — C has no methods and a member access named the
			// same as a global function is an unrelated field.
			startByte := ref.Span.StartByte
			src := resolver.Source(cand)
			if ref.Binding.Reason == "property_access" && startByte > 0 && len(src) > 0 {
				prev := src[startByte-1]
				if prev == '.' || (startByte >= 2 && src[startByte-2] == '-' && prev == '>') {
					continue
				}
			}
			out[cand] = append(out[cand], span{
				start: startByte,
				end:   ref.Span.EndByte,
				isDef: false,
			})
		}
	}

	return out, true
}
