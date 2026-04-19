package scope

// MergeDuplicateDecls unifies Decls that participate in within-file
// declaration merging: same parent scope, same name, and kinds that
// can merge. The canonical picks the first occurrence; all duplicates
// have their DeclID rewritten to match, and every Ref.Binding.Decl
// that pointed at a duplicate is rebound to the canonical.
//
// Handles three documented cases:
//   - TypeScript class+interface (or any of class/interface/type alias)
//     declared with the same name in the same scope — TS declaration
//     merging.
//   - Ruby open-class reopening within one file: `class Foo ... end`
//     followed later by another `class Foo ... end` at the same scope.
//   - C# `partial class Foo` repeated in the same file (rare; cross-file
//     partials are handled by the store-level reconciliation pass).
//
// Decls whose Kind is not mergeable (functions, methods, fields, params,
// local vars, const, import) are skipped: method overloads must stay
// distinct.
func MergeDuplicateDecls(r *Result) {
	if r == nil || len(r.Decls) < 2 {
		return
	}
	type key struct {
		parent ScopeID
		name   string
		cat    mergeCategory
	}
	canonical := make(map[key]DeclID, len(r.Decls))
	remap := make(map[DeclID]DeclID)
	for i := range r.Decls {
		d := &r.Decls[i]
		cat, ok := mergeCategoryOf(d.Kind)
		if !ok {
			continue
		}
		k := key{parent: d.Scope, name: d.Name, cat: cat}
		if canonID, found := canonical[k]; found {
			if d.ID != canonID {
				remap[d.ID] = canonID
				d.ID = canonID
			}
		} else {
			canonical[k] = d.ID
		}
	}
	if len(remap) == 0 {
		return
	}
	for i := range r.Refs {
		b := &r.Refs[i].Binding
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

// mergeCategory groups DeclKinds that can merge with each other.
// TS declaration merging permits class+interface+type-alias to unify;
// enums and namespaces stay in their own categories.
type mergeCategory int

const (
	mergeNone mergeCategory = iota
	mergeTypeOwner
	mergeEnum
	mergeNamespace
)

func mergeCategoryOf(k DeclKind) (mergeCategory, bool) {
	switch k {
	case KindClass, KindInterface, KindType:
		return mergeTypeOwner, true
	case KindEnum:
		return mergeEnum, true
	case KindNamespace:
		return mergeNamespace, true
	}
	return mergeNone, false
}
