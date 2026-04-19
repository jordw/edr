package store

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// reconcileResults performs cross-file declaration merging across all
// parsed Results. For languages that support declaration merging
// (C# partial classes, Ruby open-class reopening, TypeScript module
// declaration merging), Decls that resolve to the same qualified name
// are unified to a single canonical DeclID, and every Ref whose
// Binding.Decl points at a non-canonical duplicate is rebound.
//
// The canonical winner is the first occurrence in alphabetical file
// order (stable across runs).
//
// Within-file merging already ran inside each builder's Parse() via
// scope.MergeDuplicateDecls. This pass handles the cross-file cases
// the single-file pass cannot see.
func reconcileResults(parsed []parsedFile) {
	if len(parsed) == 0 {
		return
	}
	sort.Slice(parsed, func(i, j int) bool { return parsed[i].rel < parsed[j].rel })

	type key struct {
		lang      string
		qualifier string
		name      string
		cat       mergeCategory
	}
	canonical := make(map[key]scope.DeclID)
	remap := make(map[scope.DeclID]scope.DeclID)

	for _, p := range parsed {
		lang := languageGroup(p.rel)
		if !supportsCrossFileMerging(lang) {
			continue
		}
		owners := scopeOwners(p.result)
		for i := range p.result.Decls {
			d := &p.result.Decls[i]
			cat, ok := mergeCategoryOf(d.Kind)
			if !ok {
				continue
			}
			qual := qualifierOf(d.Scope, p.result, owners)
			k := key{lang: lang, qualifier: qual, name: d.Name, cat: cat}
			if canonID, found := canonical[k]; found {
				if d.ID != canonID {
					remap[d.ID] = canonID
					d.ID = canonID
				}
			} else {
				canonical[k] = d.ID
			}
		}
	}

	if len(remap) == 0 {
		return
	}
	for _, p := range parsed {
		for i := range p.result.Refs {
			b := &p.result.Refs[i].Binding
			if newID, ok := remap[b.Decl]; ok {
				b.Decl = newID
			}
			for j, c := range b.Candidates {
				if newID, ok := remap[c]; ok {
					b.Candidates[j] = newID
				}
			}
		}
	}
}

// languageGroup maps a file extension to a coarse language tag so
// same-name decls in, say, a Go file and a TS file are never merged.
func languageGroup(rel string) string {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".cs":
		return "csharp"
	case ".rb":
		return "ruby"
	case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts":
		return "ts"
	}
	return ""
}

func supportsCrossFileMerging(lang string) bool {
	return lang == "csharp" || lang == "ruby" || lang == "ts"
}

// mergeCategory / mergeCategoryOf mirror the within-file logic in
// internal/scope/reconcile.go. Duplicated here (rather than exported
// from scope) because cross-file merging uses identical rules but the
// scope package has no reason to expose them.
type mergeCategory int

const (
	mergeNone mergeCategory = iota
	mergeTypeOwner
	mergeEnum
	mergeNamespace
)

func mergeCategoryOf(k scope.DeclKind) (mergeCategory, bool) {
	switch k {
	case scope.KindClass, scope.KindInterface, scope.KindType:
		return mergeTypeOwner, true
	case scope.KindEnum:
		return mergeEnum, true
	case scope.KindNamespace:
		return mergeNamespace, true
	}
	return mergeNone, false
}

// scopeOwners maps each ScopeID of a class/interface/namespace scope
// to the name of the Decl that owns it (i.e., the `class Foo { ... }`
// decl opens the scope named "Foo"). Used to build qualifier chains.
func scopeOwners(r *scope.Result) map[scope.ScopeID]string {
	owners := make(map[scope.ScopeID]string)
	for _, s := range r.Scopes {
		switch s.Kind {
		case scope.ScopeClass, scope.ScopeInterface, scope.ScopeNamespace:
		default:
			continue
		}
		// Pick the Decl whose Scope is the scope's Parent, whose Kind
		// matches, and whose span ends closest before the scope starts.
		var best *scope.Decl
		for i := range r.Decls {
			d := &r.Decls[i]
			if d.Scope != s.Parent {
				continue
			}
			if !isScopeOwnerKind(d.Kind, s.Kind) {
				continue
			}
			if d.Span.EndByte > s.Span.StartByte {
				continue
			}
			if best == nil || d.Span.EndByte > best.Span.EndByte {
				best = d
			}
		}
		if best != nil {
			owners[s.ID] = best.Name
		}
	}
	return owners
}

func isScopeOwnerKind(declKind scope.DeclKind, scopeKind scope.ScopeKind) bool {
	switch scopeKind {
	case scope.ScopeClass:
		return declKind == scope.KindClass || declKind == scope.KindType
	case scope.ScopeInterface:
		return declKind == scope.KindInterface
	case scope.ScopeNamespace:
		return declKind == scope.KindNamespace
	}
	return false
}

// qualifierOf walks up the scope chain from sc and joins owner names
// with "." to produce a stable cross-file qualifier key. Starting from
// sc (the scope in which the decl lives), each ancestor class/namespace
// contributes its owner name.
func qualifierOf(sc scope.ScopeID, r *scope.Result, owners map[scope.ScopeID]string) string {
	parents := make(map[scope.ScopeID]scope.ScopeID, len(r.Scopes))
	for _, s := range r.Scopes {
		parents[s.ID] = s.Parent
	}
	var parts []string
	cur := sc
	// Safety cap in case of a malformed parent cycle.
	for depth := 0; cur != 0 && depth < 256; depth++ {
		if name, ok := owners[cur]; ok {
			parts = append([]string{name}, parts...)
		}
		next, ok := parents[cur]
		if !ok || next == cur {
			break
		}
		cur = next
	}
	return strings.Join(parts, ".")
}
