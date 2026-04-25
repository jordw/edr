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
		// Hierarchy-aware: accept call sites through a
		// supertype/subtype-typed receiver.
		owners := swiftBuildScopeOwners(targetRes, resolver.Source(sym.File))
		supMap := map[string][]string{}
		subMap := map[string][]string{}
		for _, o := range owners {
			supMap[o.owner] = append(supMap[o.owner], o.supers...)
			for _, s := range o.supers {
				subMap[s] = append(subMap[s], o.owner)
			}
		}
		stack := []string{sym.Receiver}
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, sup := range supMap[n] {
				if !acceptableTypes[sup] {
					acceptableTypes[sup] = true
					stack = append(stack, sup)
				}
			}
			for _, sub := range subMap[n] {
				if !acceptableTypes[sub] {
					acceptableTypes[sub] = true
					stack = append(stack, sub)
				}
			}
		}
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
			// Property-access handling: accept `obj.method` when obj's
			// type is in acceptableTypes; span stays identifier-only.
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

func swift_ext_matches(file string, exts []string) bool {
	e := strings.ToLower(file)
	for _, ext := range exts {
		if strings.HasSuffix(e, ext) {
			return true
		}
	}
	return false
}


// swiftScopeOwner describes one class/interface/struct/extension
// scope: the owner name (its own for class/interface, the target
// for `extension X`), and the list of super types declared via
// `: Foo, Bar` after the owner name.
type swiftScopeOwner struct {
	scopeID scope.ScopeID
	owner   string
	supers  []string
}

// swiftBuildScopeOwners returns one entry per class/interface scope
// in res. Each entry has the owner name and the superclass/protocol
// list parsed from refs preceding the scope's `{`.
func swiftBuildScopeOwners(res *scope.Result, src []byte) []swiftScopeOwner {
	if res == nil {
		return nil
	}
	var out []swiftScopeOwner
	for si := range res.Scopes {
		s := &res.Scopes[si]
		if s.Kind != scope.ScopeInterface && s.Kind != scope.ScopeClass {
			continue
		}
		// Owner detection:
		//  Case A: the class/interface decl whose FullSpan contains
		//  this scope. Its Name is the owner.
		//  Case B: an `extension X` block. The last ref to an ident
		//  immediately before the scope's `{` (through optional `:`,
		//  `,`, and whitespace) is part of the super list; for the
		//  owner we look for an "extension" keyword token in src
		//  prior to the refs, falling back to the last ref name.
		var owner string
		for i := range res.Decls {
			d := &res.Decls[i]
			if d.Kind != scope.KindInterface && d.Kind != scope.KindClass {
				continue
			}
			if d.FullSpan.StartByte <= s.Span.StartByte && s.Span.EndByte <= d.FullSpan.EndByte {
				if d.Span.EndByte <= s.Span.StartByte {
					owner = d.Name
					break
				}
			}
		}
		// Parse supers: walk refs between owner-decl end (or scope
		// start if owner unknown) and scope.Span.StartByte, each
		// separated by `:` or `,` + whitespace.
		var supers []string
		if len(src) > 0 {
			end := int(s.Span.StartByte)
			start := 0
			if owner != "" {
				for i := range res.Decls {
					d := &res.Decls[i]
					if d.Name != owner {
						continue
					}
					if d.Kind != scope.KindInterface && d.Kind != scope.KindClass {
						continue
					}
					if d.FullSpan.StartByte <= s.Span.StartByte && s.Span.EndByte <= d.FullSpan.EndByte {
						start = int(d.Span.EndByte)
						break
					}
				}
			}
			if start < end {
				// Find refs in the window [start, end).
				for i := range res.Refs {
					ref := &res.Refs[i]
					if int(ref.Span.StartByte) < start || int(ref.Span.EndByte) > end {
						continue
					}
					supers = append(supers, ref.Name)
				}
			}
		}
		// Extension detection: no owner-decl contained the scope.
		// Look for a lone `extension X ... {` pattern — the first
		// ident after the `extension` keyword is both owner and
		// not a super.
		if owner == "" {
			// Scan back from scope.Span.StartByte for "extension".
			s2 := string(src)
			i := int(s.Span.StartByte) - 1
			limit := i - 200
			if limit < 0 {
				limit = 0
			}
			for i >= limit {
				if i+9 <= len(s2) && s2[i:i+9] == "extension" {
					// The next ident after i+9 is the target type.
					j := i + 9
					for j < len(s2) && (s2[j] == ' ' || s2[j] == '\t') {
						j++
					}
					start := j
					for j < len(s2) && ((s2[j] >= 'a' && s2[j] <= 'z') || (s2[j] >= 'A' && s2[j] <= 'Z') || (s2[j] >= '0' && s2[j] <= '9') || s2[j] == '_') {
						j++
					}
					if j > start {
						owner = s2[start:j]
						// Exclude owner from supers (it's the target).
						filtered := supers[:0]
						for _, sup := range supers {
							if sup != owner {
								filtered = append(filtered, sup)
							}
						}
						supers = filtered
					}
					break
				}
				i--
			}
		}
		if owner != "" {
			out = append(out, swiftScopeOwner{scopeID: s.ID, owner: owner, supers: supers})
		}
	}
	return out
}

// swiftSiblingMethodDecls returns method decls in res whose name
// matches methodName AND whose enclosing scope is owned by a type
// transitively related to ownerName (via extends/conforms in
// either direction).
func swiftSiblingMethodDecls(res *scope.Result, src []byte, methodName, ownerName string) []*scope.Decl {
	if res == nil {
		return nil
	}
	owners := swiftBuildScopeOwners(res, src)
	// Build hierarchy maps.
	nameToScopes := map[string][]scope.ScopeID{}
	nameToSupers := map[string][]string{}
	subtypesByName := map[string][]string{}
	for _, o := range owners {
		nameToScopes[o.owner] = append(nameToScopes[o.owner], o.scopeID)
		nameToSupers[o.owner] = append(nameToSupers[o.owner], o.supers...)
		for _, sup := range o.supers {
			subtypesByName[sup] = append(subtypesByName[sup], o.owner)
		}
	}
	// Transitive walk — up and down from ownerName.
	related := map[string]bool{ownerName: true}
	stack := []string{ownerName}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, sup := range nameToSupers[n] {
			if !related[sup] {
				related[sup] = true
				stack = append(stack, sup)
			}
		}
		for _, sub := range subtypesByName[n] {
			if !related[sub] {
				related[sub] = true
				stack = append(stack, sub)
			}
		}
	}
	ownedScopes := map[scope.ScopeID]bool{}
	for name := range related {
		for _, sid := range nameToScopes[name] {
			ownedScopes[sid] = true
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
