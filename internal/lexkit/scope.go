package lexkit

// Scope is a single entry in a ScopeStack. Data carries
// language-specific information (e.g., an enum distinguishing class
// bodies from function bodies). SymIdx is the index of the associated
// symbol in the parser's symbol slice, or -1 if this scope doesn't
// correspond to a named symbol (e.g., a plain block).
type Scope[T any] struct {
	Data     T
	SymIdx   int
	OpenLine int
}

// ScopeStack is a LIFO stack of Scope entries. Parsers push a scope when
// entering a container (class body, function body, namespace, etc.) and
// pop when the container closes, updating the symbol's EndLine.
type ScopeStack[T any] struct {
	entries []Scope[T]
}

// Push adds a scope to the top of the stack.
func (ss *ScopeStack[T]) Push(e Scope[T]) {
	ss.entries = append(ss.entries, e)
}

// Pop removes and returns the top scope, or zero value and false if the
// stack is empty.
func (ss *ScopeStack[T]) Pop() (Scope[T], bool) {
	n := len(ss.entries)
	if n == 0 {
		var zero Scope[T]
		return zero, false
	}
	e := ss.entries[n-1]
	ss.entries = ss.entries[:n-1]
	return e, true
}

// Top returns a pointer to the top scope, or nil if the stack is empty.
// The pointer is invalidated by any subsequent Push or Pop.
func (ss *ScopeStack[T]) Top() *Scope[T] {
	n := len(ss.entries)
	if n == 0 {
		return nil
	}
	return &ss.entries[n-1]
}

// Depth returns the number of scopes currently on the stack.
func (ss *ScopeStack[T]) Depth() int { return len(ss.entries) }

// Any reports whether any scope on the stack satisfies pred.
func (ss *ScopeStack[T]) Any(pred func(T) bool) bool {
	for i := range ss.entries {
		if pred(ss.entries[i].Data) {
			return true
		}
	}
	return false
}

// NearestSym returns the SymIdx of the nearest enclosing scope that has
// one, or -1 if none of the scopes on the stack are associated with a
// symbol.
func (ss *ScopeStack[T]) NearestSym() int {
	for i := len(ss.entries) - 1; i >= 0; i-- {
		if ss.entries[i].SymIdx >= 0 {
			return ss.entries[i].SymIdx
		}
	}
	return -1
}

// Entries returns a read-only view of the stack from bottom to top. The
// slice must not be modified and is invalidated by subsequent Push/Pop.
func (ss *ScopeStack[T]) Entries() []Scope[T] { return ss.entries }