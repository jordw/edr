// Package builtins provides per-language sets of globally-visible
// identifiers (type names, value names, utility types) that a scope
// builder should bind with lower fidelity than user-defined decls.
//
// A ref that doesn't resolve via the scope chain but matches a builtin
// name gets BindResolved with Reason="builtin" and a deterministic
// synthetic DeclID. Consumers (rename, refs-to) can filter on the
// Reason to distinguish user code from language-level names.
package builtins

// Set is an immutable set of identifier names.
type Set map[string]struct{}

// Has reports whether the name is in the set.
func (s Set) Has(name string) bool {
	_, ok := s[name]
	return ok
}

// from constructs a Set from a variadic list of names.
func from(names ...string) Set {
	s := make(Set, len(names))
	for _, n := range names {
		s[n] = struct{}{}
	}
	return s
}
