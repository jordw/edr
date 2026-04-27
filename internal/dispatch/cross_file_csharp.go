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
		var extraCandidates []string
		if refs, err := db.FindSemanticReferences(ctx, sym.Receiver, sym.File); err == nil {
			seen := map[string]struct{}{sym.File: {}}
			for _, r := range refs {
				if _, ok := seen[r.File]; ok {
					continue
				}
				seen[r.File] = struct{}{}
				extraCandidates = append(extraCandidates, r.File)
			}
		}
		EmitOverrideSpans(out, csharpResolverDeps{r: resolver}, sym, extraCandidates...)
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


// csharpResolverDeps adapts CSharpResolver to the dispatch
// HierarchyDeps interface used by EmitOverrideSpans.
type csharpResolverDeps struct {
	r *namespace.CSharpResolver
}

func (d csharpResolverDeps) Result(file string) *scope.Result { return d.r.Result(file) }
func (d csharpResolverDeps) SamePackageFiles(file string) []string {
	return d.r.SamePackageFiles(file)
}
func (d csharpResolverDeps) FilesForImport(spec, importingFile string) []string {
	return d.r.FilesForImport(spec, importingFile)
}

// ImportSpec rebuilds a C# `using Foo.Bar;` spec from a KindImport
// decl. The CS builder stamps Signature with the dotted module
// path; the imported binding name (when aliased via `using
// Alias = Foo.Bar`) is the alias, not the spec.
func (d csharpResolverDeps) ImportSpec(decl *scope.Decl) string {
	module, _ := SplitImportSignature(decl)
	return module
}
