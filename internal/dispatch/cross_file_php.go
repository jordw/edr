package dispatch

import (
	"context"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/namespace"
)

// phpCrossFileSpans is the PHP branch of
// scopeAwareCrossFileSpans. Uses canonical-DeclID matching via the
// namespace abstraction; falls through (returns empty map) when no
// cross-file matches are found, so the dispatch switch can defer
// to the generic ref-filtering path for cases this resolver
// doesn't model yet.
func phpCrossFileSpans(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) (map[string][]span, bool) {
	if !php_ext_matches(sym.File, []string{".php", ".phtml"}) {
		return nil, false
	}
	out := map[string][]span{}
	resolver := namespace.NewPHPResolver(db.Root())
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
		phpEmitHierarchySpans(out, resolver, sym, target)
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
		for _, t := range namespace.PHPRelatedTypes(resolver.Source(sym.File), sym.Receiver) {
			acceptableTypes[t] = true
		}
	}

	pop := namespace.PHPPopulator(resolver)
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
			for k, v := range varTypes {
				if len(k) > 0 && k[0] == '$' {
					varTypes[k[1:]] = v
				}
			}
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
				isArrow := ref.Span.StartByte >= 2 && src[ref.Span.StartByte-2] == '-' && prev == '>'
				isScope := ref.Span.StartByte >= 2 && src[ref.Span.StartByte-2] == ':' && prev == ':'
				isDot := prev == '.'
				if !isArrow && !isScope && !isDot {
					continue
				}
				if !isMethod {
					continue
				}
				baseIdent := phpBaseIdentBefore(src, ref.Span.StartByte, isArrow, isScope)
				if baseIdent == "" {
					continue
				}
				if !acceptableTypes[varTypes[baseIdent]] && !acceptableTypes[baseIdent] {
					continue
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

func php_ext_matches(file string, exts []string) bool {
	e := strings.ToLower(file)
	for _, ext := range exts {
		if strings.HasSuffix(e, ext) {
			return true
		}
	}
	return false
}


// phpBaseIdentBefore returns the identifier before `->` / `::` / `.`
// at refStart, stripping any leading `$` sigil so a PHP variable
// like `$obj` maps to just `obj` for varTypes lookup.
func phpBaseIdentBefore(src []byte, refStart uint32, isArrow, isScope bool) string {
	if int(refStart) <= 0 || int(refStart) > len(src) {
		return ""
	}
	end := int(refStart) - 1 // at `.`, `>`, or second `:`
	if isArrow || isScope {
		end-- // skip to the first char of the 2-char operator
	}
	i := end - 1
	for i >= 0 {
		c := src[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' {
			i--
			continue
		}
		break
	}
	name := string(src[i+1 : end])
	// PHP: variables are written `$name`; the scanner above stops at
	// `$` (not an ident byte), so name already excludes the sigil.
	return name
}


func phpEmitHierarchySpans(out map[string][]span, resolver *namespace.PHPResolver, sym *index.SymbolInfo, target *scope.Decl) {
	src := resolver.Source(sym.File)
	if len(src) == 0 {
		return
	}
	hier := namespace.PHPFindClassHierarchy(src)
	if len(hier) == 0 {
		return
	}
	var enclosing *namespace.PHPClassHierarchy
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
	for _, t := range namespace.PHPRelatedTypes(src, enclosing.Name) {
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
