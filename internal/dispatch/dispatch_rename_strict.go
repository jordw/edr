package dispatch

import (
	"os"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	scopestore "github.com/jordw/edr/internal/scope/store"
)

// strictRefusedRef is one entry in a strict-mode refusal report. It
// names a single ref that would have been rewritten under default /
// --force semantics but isn't binding-confirmed to BindResolved, so
// strict mode declines to act on it.
type strictRefusedRef struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Tier   string `json:"tier"`             // "ambiguous" | "probable" | "unresolved"
	Reason string `json:"reason,omitempty"` // Binding.Reason, if set
}

// strictAudit summarises the binding-tier composition of refs that
// would be rewritten by a rename. Used by --strict to decide whether
// to refuse, and surfaced in the refused-output payload so callers
// understand why.
type strictAudit struct {
	Resolved int
	Counts   map[string]int     // tier name → count
	Refused  []strictRefusedRef // sample entries, capped
}

// strictAuditSampleCap limits how many refused-ref entries we surface
// in the refusal payload. Counts in `Counts` are exact regardless.
const strictAuditSampleCap = 10

// auditSameFileBinding parses the target file with the scope builder,
// finds the rename target, and partitions same-file refs by binding
// tier. Returns whether the audit could run (false on parse failure
// or when the language isn't scope-supported — strict callers should
// refuse on `ok=false` rather than silently let through).
func auditSameFileBinding(sym *index.SymbolInfo) (strictAudit, bool) {
	if !scopeSupported(sym.File) {
		return strictAudit{}, false
	}
	src, err := os.ReadFile(sym.File)
	if err != nil {
		return strictAudit{}, false
	}
	result := scopestore.Parse(sym.File, src)
	if result == nil {
		return strictAudit{}, false
	}

	var target *scope.Decl
	for i := range result.Decls {
		d := &result.Decls[i]
		if d.Name != sym.Name {
			continue
		}
		if d.Span.StartByte == sym.StartByte {
			target = d
			break
		}
		if sym.StartByte <= d.Span.StartByte && d.Span.EndByte <= sym.EndByte {
			if target == nil {
				target = d
			}
		}
	}
	if target == nil {
		return strictAudit{}, false
	}

	audit := strictAudit{Counts: map[string]int{}}
	lines := computeLineStarts(src)
	rel := sym.File

	addRefused := func(span scope.Span, tier, reason string) {
		audit.Counts[tier]++
		if len(audit.Refused) < strictAuditSampleCap {
			line, _ := byteToLineCol(lines, span.StartByte)
			audit.Refused = append(audit.Refused, strictRefusedRef{
				File:   rel,
				Line:   line,
				Tier:   tier,
				Reason: reason,
			})
		}
	}

	for _, ref := range result.Refs {
		// Only refs that name-match the target are in scope for the
		// rewrite — strict gates on them, the rest are unrelated.
		if ref.Name != sym.Name {
			continue
		}
		switch ref.Binding.Kind {
		case scope.BindResolved:
			if ref.Binding.Decl == target.ID {
				audit.Resolved++
			}
		case scope.BindAmbiguous:
			// Any name-matching ambiguous ref blocks strict, regardless
			// of whether the candidate set contains target.ID — strict
			// can't tell which decl the call dispatches to, and the
			// language renamers may emit it as a candidate match
			// downstream.
			addRefused(ref.Span, "ambiguous", ref.Binding.Reason)
		case scope.BindProbable:
			// Same logic. Property-access refs (Ruby/Python obj.method,
			// Lua/Zig namespaced calls) typically have no Decl set —
			// strict treats them as not binding-confirmed.
			addRefused(ref.Span, "probable", ref.Binding.Reason)
		case scope.BindUnresolved:
			// Name-matching unresolved refs aren't in the rewrite set,
			// but they're a strict-mode signal: the user almost
			// certainly meant for the rename to cover them, and
			// silently skipping would violate the strict contract.
			addRefused(ref.Span, "unresolved", ref.Binding.Reason)
		}
	}

	return audit, true
}

// nonResolvedTotal returns the sum across ambiguous/probable/unresolved
// tiers — i.e. how many refs strict mode would block on.
func (a strictAudit) nonResolvedTotal() int {
	n := 0
	for _, v := range a.Counts {
		n += v
	}
	return n
}
