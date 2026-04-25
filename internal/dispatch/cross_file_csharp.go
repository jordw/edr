package dispatch

import (
	"context"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/namespace"
)

// csharpCrossFileSpans is the CSharp branch of
// scopeAwareCrossFileSpans. Uses canonical-DeclID matching via the
// namespace abstraction; falls through (returns empty map) when no
// cross-file matches are found, so the dispatch switch can defer
// to the generic ref-filtering path for cases this resolver
// doesn't model yet.
func csharpCrossFileSpans(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) (map[string][]span, bool) {
	if !strings.EqualFold(sym.File[len(sym.File)-3:], ".cs") {
		return nil, false
	}
	out := map[string][]span{}
	resolver := namespace.NewCSharpResolver(db.Root())
	canonical := resolver.CanonicalPath(sym.File)
	targetRes := resolver.Result(sym.File)
	if canonical == "" || targetRes == nil {
		return nil, false
	}

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

	if sym.Receiver != "" {
		csEmitHierarchySpans(out, resolver, sym, target)
	}

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

	isMethod := sym.Receiver != ""
	acceptableTypes := map[string]bool{}
	if sym.Receiver != "" {
		acceptableTypes[sym.Receiver] = true
		for _, t := range namespace.CSRelatedTypes(resolver.Source(sym.File), sym.Receiver) {
			acceptableTypes[t] = true
		}
	}

	pop := namespace.CSharpPopulator(resolver)
	for cand := range candidates {
		candRes := resolver.Result(cand)
		if candRes == nil {
			continue
		}
		ns := namespace.Build(cand, candRes, resolver, pop)
		if !isMethod && !ns.Matches(sym.Name, target.ID) {
			continue
		}
		declByID := make(map[scope.DeclID]*scope.Decl, len(candRes.Decls))
		for i := range candRes.Decls {
			declByID[candRes.Decls[i].ID] = &candRes.Decls[i]
		}
		src := resolver.Source(cand)
		var varTypes map[string]string
		if isMethod {
			varTypes = buildVarTypes(candRes, src)
		}

		// File-scope decls whose ID matches the target (the
		// canonical-path merge brings prototypes / siblings here).
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
			})
		}

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
			if ref.Binding.Reason == "property_access" && ref.Span.StartByte > 0 && len(src) > 0 {
				prev := src[ref.Span.StartByte-1]
				isDot := prev == '.'
				isOther := (ref.Span.StartByte >= 2 && src[ref.Span.StartByte-2] == '-' && prev == '>') ||
					(ref.Span.StartByte >= 2 && src[ref.Span.StartByte-2] == ':' && prev == ':')
				if isOther {
					continue
				}
				if isDot {
					if !isMethod {
						continue
					}
					baseIdent := dotBaseIdentBefore(src, ref.Span.StartByte)
					if baseIdent == "" {
						continue
					}
					if !acceptableTypes[varTypes[baseIdent]] && !acceptableTypes[baseIdent] {
						continue
					}
				}
			}
			out[cand] = append(out[cand], span{
				start: ref.Span.StartByte,
				end:   ref.Span.EndByte,
			})
		}
	}
	return out, true
}

func csharp_ext_matches(file string, exts []string) bool {
	e := strings.ToLower(file)
	for _, ext := range exts {
		if strings.HasSuffix(e, ext) {
			return true
		}
	}
	return false
}


func csEmitHierarchySpans(out map[string][]span, resolver *namespace.CSharpResolver, sym *index.SymbolInfo, target *scope.Decl) {
	src := resolver.Source(sym.File)
	if len(src) == 0 {
		return
	}
	hier := namespace.CSFindClassHierarchy(src)
	if len(hier) == 0 {
		return
	}
	var enclosing *namespace.CSClassHierarchy
	for i := range hier {
		h := &hier[i]
		if h.BodyStart <= target.Span.StartByte && target.Span.EndByte <= h.BodyEnd {
			enclosing = h
			break
		}
	}
	if enclosing == nil {
		return
	}
	related := map[string]bool{}
	for _, t := range namespace.CSRelatedTypes(src, enclosing.Name) {
		related[t] = true
	}
	if len(related) == 0 {
		return
	}
	targetRes := resolver.Result(sym.File)
	if targetRes == nil {
		return
	}
	for _, h := range hier {
		if !related[h.Name] || h.Name == enclosing.Name {
			continue
		}
		for i := range targetRes.Decls {
			d := &targetRes.Decls[i]
			if d.Name != sym.Name {
				continue
			}
			if d.Kind != scope.KindMethod && d.Kind != scope.KindFunction {
				continue
			}
			if d.Span.StartByte < h.BodyStart || d.Span.EndByte > h.BodyEnd {
				continue
			}
			if d.ID == target.ID {
				continue
			}
			out[sym.File] = append(out[sym.File], span{
				start: d.Span.StartByte,
				end:   d.Span.EndByte,
			})
		}
	}
}
