package dispatch

import (
	"context"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/namespace"
)

// swiftCrossFileSpans is the Swift branch of
// scopeAwareCrossFileSpans. Uses canonical-DeclID matching via the
// namespace abstraction; falls through (returns empty map) when no
// cross-file matches are found, so the dispatch switch can defer
// to the generic ref-filtering path for cases this resolver
// doesn't model yet.
func swiftCrossFileSpans(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) (map[string][]span, bool) {
	if !strings.HasSuffix(strings.ToLower(sym.File), ".swift") {
		return nil, false
	}
	out := map[string][]span{}
	resolver := namespace.NewSwiftResolver(db.Root())
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

	// For method renames: find sibling methods in sym.File whose
	// enclosing scope is owned by sym.Receiver (either directly as
	// the protocol/class body, or indirectly via an `extension X`
	// block that precedes the scope). These decls must rename in
	// lockstep so the protocol + default impl stay consistent.
	if sym.Receiver != "" {
		src := resolver.Source(sym.File)
		for _, sib := range swiftSiblingMethodDecls(targetRes, src, sym.Name, sym.Receiver) {
			if sib.ID == target.ID {
				continue
			}
			out[sym.File] = append(out[sym.File], span{
				start: sib.Span.StartByte,
				end:   sib.Span.EndByte,
				isDef: true,
			})
		}
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
	}

	pop := namespace.SwiftPopulator(resolver)
	for cand := range candidates {
		candRes := resolver.Result(cand)
		if candRes == nil {
			continue
		}
		ns := namespace.Build(cand, candRes, resolver, pop)
		// Methods don't live at file scope so their names aren't in
		// the namespace. Admit every candidate when renaming a
		// method and rely on per-ref disambiguation below.
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
				isDef: true,
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
			// Property-access handling. For method renames we accept
			// `obj.method` when obj's type matches acceptableTypes,
			// and expand the span back through the leading dot.
			startByte := ref.Span.StartByte
			if ref.Binding.Reason == "property_access" && startByte > 0 && len(src) > 0 {
				prev := src[startByte-1]
				isDot := prev == '.'
				// Ruby / Swift don't use `->` or `::` for instance
				// access; keep those as skip-conditions.
				isOther := (startByte >= 2 && src[startByte-2] == '-' && prev == '>') ||
					(startByte >= 2 && src[startByte-2] == ':' && prev == ':')
				if isOther {
					continue
				}
				if isDot {
					if !isMethod {
						continue
					}
					baseIdent := dotBaseIdentBefore(src, startByte)
					if baseIdent == "" {
						continue
					}
					// Accept (a) variable of an acceptable type, or
					// (b) the base ident IS an acceptable type itself
					// (class.classmethod / Module.method pattern).
					if !acceptableTypes[varTypes[baseIdent]] && !acceptableTypes[baseIdent] {
						continue
					}
					startByte--
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

func swift_ext_matches(file string, exts []string) bool {
	e := strings.ToLower(file)
	for _, ext := range exts {
		if strings.HasSuffix(e, ext) {
			return true
		}
	}
	return false
}


// swiftSiblingMethodDecls returns method decls in res whose name
// matches methodName AND whose enclosing scope is owned by a type
// named ownerName — either the body of a protocol/class/struct
// named ownerName, or an `extension ownerName { … }` block
// identified by a preceding REF to ownerName.
func swiftSiblingMethodDecls(res *scope.Result, src []byte, methodName, ownerName string) []*scope.Decl {
	if res == nil {
		return nil
	}
	// Determine which scopes are owned by ownerName.
	ownedScopes := map[scope.ScopeID]bool{}
	for si := range res.Scopes {
		s := &res.Scopes[si]
		if s.Kind != scope.ScopeInterface && s.Kind != scope.ScopeClass {
			continue
		}
		// Case 1: owned by a decl whose FullSpan contains this scope
		// and whose Name == ownerName.
		for i := range res.Decls {
			d := &res.Decls[i]
			if d.Name != ownerName {
				continue
			}
			if d.Kind != scope.KindInterface && d.Kind != scope.KindClass {
				continue
			}
			if d.FullSpan.StartByte <= s.Span.StartByte && s.Span.EndByte <= d.FullSpan.EndByte {
				ownedScopes[s.ID] = true
				break
			}
		}
		if ownedScopes[s.ID] {
			continue
		}
		// Case 2: the scope is an `extension X` block. Look for the
		// nearest ref to ownerName immediately before s.Span.StartByte.
		// Allow only whitespace / identifier-keyword bytes between
		// the ref end and the scope start (the opening `{`).
		for i := range res.Refs {
			ref := &res.Refs[i]
			if ref.Name != ownerName {
				continue
			}
			if ref.Span.EndByte >= s.Span.StartByte {
				continue
			}
			// Gap must be whitespace; tolerating `:` (conformance
			// list in `struct Foo: Bar {}`) is NOT desired for
			// extensions, but ordinary `extension X {` has only
			// whitespace between the Greeter ref and the `{`.
			if s.Span.StartByte > 0 && len(src) > 0 {
				gap := src[ref.Span.EndByte:s.Span.StartByte]
				allowed := true
				for _, c := range gap {
					if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
						continue
					}
					allowed = false
					break
				}
				if allowed {
					ownedScopes[s.ID] = true
					break
				}
			}
		}
	}
	// Collect matching methods.
	var out []*scope.Decl
	for i := range res.Decls {
		d := &res.Decls[i]
		if d.Kind != scope.KindMethod || d.Name != methodName {
			continue
		}
		if !ownedScopes[d.Scope] {
			continue
		}
		out = append(out, d)
	}
	return out
}
