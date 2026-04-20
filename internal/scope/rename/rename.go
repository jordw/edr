// Package rename implements precise, single-file symbol rename on top of
// scope.Result. This is edr's first destructive feature.
//
// Plan returns two pointer values, at most one of which is non-nil.
// Apply is declared only on *Ready — there is no Apply on *Refused,
// so a refusal cannot be applied by mistake. Callers are expected to
// check the *Refused return before dereferencing *Ready; Go's nil-
// pointer semantics will panic otherwise, which is the correct
// behavior for a contract violation.
package rename

import (
	"fmt"

	"github.com/jordw/edr/internal/scope"
)

// Edit is a byte-range substitution in the planned file.
type Edit struct {
	StartByte uint32
	EndByte   uint32
	NewText   string
}

// Ready carries a rename plan that has passed every safety check and
// can be applied without further user confirmation.
type Ready struct {
	File    string
	OldName string
	NewName string
	DeclID  scope.DeclID
	Edits   []Edit
	src     []byte
}

// Refused carries a refusal reason. Reason codes are the authoritative
// surface; callers should pattern-match on Reason rather than parsing
// Detail. Candidates is populated when multiple decls could plausibly
// host the span (no_symbol_at_span on overlap) or when the target decl
// has non-Resolved refs (mixed_confidence).
type Refused struct {
	Reason     string
	Detail     string
	Candidates []scope.Decl
}

// Refusal reason codes. Stable identifiers, not human strings.
const (
	ReasonNoSymbolAtSpan   = "no_symbol_at_span"
	ReasonMixedConfidence  = "mixed_confidence"
	ReasonCollision        = "collision"
	ReasonInvalidNewName   = "invalid_new_name"
	ReasonNoChange         = "no_change"
	ReasonStaleIndex       = "stale_index"
	ReasonOverlappingEdits = "overlapping_edits"
)

// Plan computes a single-file rename for the decl at span. file is the
// scope result for src (produced by the appropriate language builder);
// span points to an occurrence of the old name (either the declaring
// identifier or a reference to it); newName is the replacement.
//
// Policy (deliberately narrow):
//
//   - The span must lie within exactly one Decl.Span or Ref.Span. No
//     decl under span -> no_symbol_at_span.
//   - Every Ref to the resolved decl must have Binding.Kind ==
//     BindResolved. Any weaker binding -> mixed_confidence.
//   - newName must be a valid JS/TS identifier and not a reserved word.
//     Otherwise -> invalid_new_name.
//   - newName must not collide with any other decl visible from the
//     target decl's scope chain, nor shadow an inner decl that the
//     target's refs would then resolve to. -> collision.
//   - newName == OldName -> no_change.
func Plan(file *scope.Result, src []byte, span scope.Span, newName string) (*Ready, *Refused) {
	if file == nil {
		return nil, &Refused{Reason: ReasonNoSymbolAtSpan, Detail: "nil scope result"}
	}

	target := locateTarget(file, span)
	if target == nil {
		return nil, &Refused{
			Reason: ReasonNoSymbolAtSpan,
			Detail: fmt.Sprintf("no decl or ref covers span [%d:%d)", span.StartByte, span.EndByte),
		}
	}

	if target.Name == newName {
		return nil, &Refused{Reason: ReasonNoChange, Detail: "newName matches oldName"}
	}

	if !isValidTSIdent(newName) || isTSReservedWord(newName) {
		return nil, &Refused{
			Reason: ReasonInvalidNewName,
			Detail: fmt.Sprintf("%q is not a valid TS identifier or is reserved", newName),
		}
	}

	allRefs := scope.RefsToDecl(file, target.ID)
	for _, r := range allRefs {
		if r.Binding.Kind != scope.BindResolved {
			return nil, &Refused{
				Reason: ReasonMixedConfidence,
				Detail: fmt.Sprintf("ref %q at [%d:%d) has binding kind %v (reason=%q)",
					r.Name, r.Span.StartByte, r.Span.EndByte, r.Binding.Kind, r.Binding.Reason),
			}
		}
	}

	if blocker := detectCollision(file, target, newName); blocker != nil {
		return nil, &Refused{
			Reason:     ReasonCollision,
			Detail:     fmt.Sprintf("newName %q collides with %s decl in scope %d", newName, blocker.Kind, blocker.Scope),
			Candidates: []scope.Decl{*blocker},
		}
	}

	// Collect edits. Every ref is already proven BindResolved (above),
	// so Reason-based filtering would be redundant — and wrong if a
	// future builder emits, say, a qualified_member ref that legitimately
	// resolves to target. De-dup vs the target's own decl span below.
	edits := make([]Edit, 0, 1+len(allRefs))
	edits = append(edits, Edit{StartByte: target.Span.StartByte, EndByte: target.Span.EndByte, NewText: newName})
	for _, r := range allRefs {
		if r.Span.StartByte == target.Span.StartByte && r.Span.EndByte == target.Span.EndByte {
			continue
		}
		edits = append(edits, Edit{StartByte: r.Span.StartByte, EndByte: r.Span.EndByte, NewText: newName})
	}

	// Each planned edit must actually spell OldName in src. A mismatch
	// means the scope result and src disagree — the index is stale
	// relative to the bytes we're about to write.
	for _, e := range edits {
		if int(e.EndByte) > len(src) || int(e.StartByte) > int(e.EndByte) {
			return nil, &Refused{
				Reason: ReasonStaleIndex,
				Detail: fmt.Sprintf("edit span [%d:%d) out of range for src len %d", e.StartByte, e.EndByte, len(src)),
			}
		}
		if string(src[e.StartByte:e.EndByte]) != target.Name {
			return nil, &Refused{
				Reason: ReasonStaleIndex,
				Detail: fmt.Sprintf("edit span [%d:%d) reads %q, expected %q", e.StartByte, e.EndByte, src[e.StartByte:e.EndByte], target.Name),
			}
		}
	}

	sorted := dedupSortEdits(edits)
	// Overlap check: after sort+dedup, consecutive edits with ranges
	// that touch or cross would make Apply produce wrong bytes. Exact
	// duplicates were removed by dedup; this catches strict overlap.
	for i := 1; i < len(sorted); i++ {
		if sorted[i].StartByte < sorted[i-1].EndByte {
			return nil, &Refused{
				Reason: ReasonOverlappingEdits,
				Detail: fmt.Sprintf("edits [%d:%d) and [%d:%d) overlap",
					sorted[i-1].StartByte, sorted[i-1].EndByte, sorted[i].StartByte, sorted[i].EndByte),
			}
		}
	}

	return &Ready{
		File:    file.File,
		OldName: target.Name,
		NewName: newName,
		DeclID:  target.ID,
		Edits:   sorted,
		src:     src,
	}, nil
}

// Apply produces the rewritten source for the target file. It does NOT
// touch disk; callers that want an on-disk edit should hand the Edits
// to edit.Transaction (so TOCTOU hash-guards apply). Apply is declared
// on *Ready, not on PlanResult — this is the compile-time guarantee
// that a refusal cannot be applied.
func (r *Ready) Apply() []byte {
	if r == nil {
		return nil
	}
	out := make([]byte, 0, len(r.src))
	cursor := uint32(0)
	for _, e := range r.Edits {
		if e.StartByte > cursor {
			out = append(out, r.src[cursor:e.StartByte]...)
		}
		out = append(out, e.NewText...)
		cursor = e.EndByte
	}
	if int(cursor) < len(r.src) {
		out = append(out, r.src[cursor:]...)
	}
	return out
}

// locateTarget finds the Decl that owns span. A span inside a Decl's
// identifier resolves to that decl directly. A span inside a Ref that
// resolved to a decl returns that decl. Overlapping Decl matches pick
// the innermost (smallest span), so a class's method-identifier span
// wins over the class's outer span.
func locateTarget(file *scope.Result, span scope.Span) *scope.Decl {
	var best *scope.Decl
	bestLen := uint32(0xFFFFFFFF)

	for i := range file.Decls {
		d := &file.Decls[i]
		if spanCovers(d.Span, span) {
			w := d.Span.EndByte - d.Span.StartByte
			if w < bestLen {
				bestLen = w
				best = d
			}
		}
	}
	if best != nil {
		return best
	}

	for i := range file.Refs {
		r := &file.Refs[i]
		if !spanCovers(r.Span, span) {
			continue
		}
		if r.Binding.Kind != scope.BindResolved {
			continue
		}
		for j := range file.Decls {
			if file.Decls[j].ID == r.Binding.Decl {
				return &file.Decls[j]
			}
		}
	}
	return nil
}

func spanCovers(outer, inner scope.Span) bool {
	return outer.StartByte <= inner.StartByte && inner.EndByte <= outer.EndByte && outer.EndByte > outer.StartByte
}

// detectCollision enforces three narrow rules; any of them refuses.
//
//   (a) Same-scope sibling: another decl with the same name+namespace
//       in target.Scope. A direct duplicate-declaration error post-rename.
//   (b) Shadower-of-target-refs: a decl named newName lives in a
//       descendant scope that contains a Ref bound to target. After
//       rename, that ref would resolve to the inner decl instead of
//       the renamed target.
//   (c) Captured ancestor-ref: a Ref inside target.Scope's subtree is
//       currently bound to an ancestor decl named newName. After
//       rename, that ref would resolve to the renamed target (we'd
//       accidentally steal a reference that used to point outward).
//
// An ancestor decl named newName alone is NOT a collision: a new local
// binding inside target.Scope legitimately shadows it. Only the
// capture case (c) makes the ancestor a problem.
func detectCollision(file *scope.Result, target *scope.Decl, newName string) *scope.Decl {
	parents := scopeParents(file)

	inSubtreeOfTarget := func(sid scope.ScopeID) bool {
		c := sid
		for {
			if c == target.Scope {
				return true
			}
			p, ok := parents[c]
			if !ok || c == 0 {
				return false
			}
			c = p
		}
	}

	// (a) same-scope sibling
	for i := range file.Decls {
		d := &file.Decls[i]
		if d.ID == target.ID {
			continue
		}
		if d.Scope == target.Scope && d.Name == newName && d.Namespace == target.Namespace {
			return d
		}
	}

	// (b) shadower of target refs: a decl named newName in a descendant
	// scope that encloses any resolved ref to target.
	targetRefs := scope.RefsToDecl(file, target.ID)
	for i := range file.Decls {
		d := &file.Decls[i]
		if d.ID == target.ID || d.Name != newName || d.Namespace != target.Namespace {
			continue
		}
		if !inSubtreeOfTarget(d.Scope) || d.Scope == target.Scope {
			continue
		}
		for _, r := range targetRefs {
			if inScopeSubtree(parents, r.Scope, d.Scope) {
				return d
			}
		}
	}

	// (c) captured ancestor-ref: a Ref in target.Scope's subtree whose
	// current binding points to a decl named newName in an ancestor.
	for i := range file.Refs {
		r := &file.Refs[i]
		if !inSubtreeOfTarget(r.Scope) {
			continue
		}
		if r.Binding.Kind != scope.BindResolved {
			continue
		}
		if r.Binding.Decl == target.ID {
			continue
		}
		for j := range file.Decls {
			d := &file.Decls[j]
			if d.ID != r.Binding.Decl {
				continue
			}
			if d.Name == newName && d.Namespace == target.Namespace {
				return d
			}
			break
		}
	}

	return nil
}

func inScopeSubtree(parents map[scope.ScopeID]scope.ScopeID, node, root scope.ScopeID) bool {
	c := node
	for {
		if c == root {
			return true
		}
		p, ok := parents[c]
		if !ok || c == 0 {
			return false
		}
		c = p
	}
}

func scopeParents(file *scope.Result) map[scope.ScopeID]scope.ScopeID {
	out := make(map[scope.ScopeID]scope.ScopeID, len(file.Scopes))
	for _, s := range file.Scopes {
		out[s.ID] = s.Parent
	}
	return out
}

func dedupSortEdits(in []Edit) []Edit {
	seen := make(map[[2]uint32]bool, len(in))
	out := make([]Edit, 0, len(in))
	for _, e := range in {
		k := [2]uint32{e.StartByte, e.EndByte}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	// Insertion sort by StartByte ascending.
	for i := 1; i < len(out); i++ {
		j := i
		for j > 0 && out[j-1].StartByte > out[j].StartByte {
			out[j-1], out[j] = out[j], out[j-1]
			j--
		}
	}
	return out
}
