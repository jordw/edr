package dispatch

import (
	"os"
	"sort"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
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

// auditFileBindings walks every file the rename would touch (the keys
// of fileSpans), parses each via the scope builder, and partitions
// name-matching refs by binding tier. The aggregate is what --strict
// gates on: any non-Resolved entry refuses. Returns ok=false if no
// file in fileSpans could be audited (parse failure / unsupported
// language); strict callers refuse on ok=false rather than silently
// let through.
//
// Why per-file rather than per-span: spans in fileSpans don't carry
// binding-kind metadata, so we re-parse and look up by name. Walking
// every name-matching ref (rather than only the spans the renamer
// chose) is over-conservative on purpose — strict's contract is
// "every byte changed is binding-confirmed AND no ambient ambiguity
// in touched files." The renamer may correctly skip some ambiguous
// refs but strict still surfaces them so the caller knows the file
// has untyped call sites worth a manual look.
func auditFileBindings(fileSpans map[string][]span, sym *index.SymbolInfo) (strictAudit, bool) {
	aggregate := strictAudit{Counts: map[string]int{}}
	anyOK := false

	files := make([]string, 0, len(fileSpans))
	for f := range fileSpans {
		files = append(files, f)
	}
	sort.Strings(files)

	for _, file := range files {
		a, ok := auditOneFile(file, sym)
		if !ok {
			continue
		}
		anyOK = true
		aggregate.Resolved += a.Resolved
		for k, v := range a.Counts {
			aggregate.Counts[k] += v
		}
		for _, r := range a.Refused {
			if len(aggregate.Refused) >= strictAuditSampleCap {
				break
			}
			aggregate.Refused = append(aggregate.Refused, r)
		}
	}
	return aggregate, anyOK
}

// auditOneFile parses `file` and tier-counts its name-matching refs.
// In the target file (file == sym.File) the Resolved counter is
// gated on the actual target DeclID so unrelated same-name decls
// don't inflate the count. In other files there is no target decl
// to match against, so any name-matching Resolved ref counts.
func auditOneFile(file string, sym *index.SymbolInfo) (strictAudit, bool) {
	if !scopeSupported(file) {
		return strictAudit{}, false
	}
	src, err := os.ReadFile(file)
	if err != nil {
		return strictAudit{}, false
	}
	result := scopestore.Parse(file, src)
	if result == nil {
		return strictAudit{}, false
	}

	var targetID scope.DeclID
	if file == sym.File {
		for i := range result.Decls {
			d := &result.Decls[i]
			if d.Name != sym.Name {
				continue
			}
			if d.Span.StartByte == sym.StartByte {
				targetID = d.ID
				break
			}
			if sym.StartByte <= d.Span.StartByte && d.Span.EndByte <= sym.EndByte {
				if targetID == 0 {
					targetID = d.ID
				}
			}
		}
		if targetID == 0 {
			return strictAudit{}, false
		}
	}

	audit := strictAudit{Counts: map[string]int{}}
	lines := computeLineStarts(src)
	rel := output.Rel(file)

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
		if ref.Name != sym.Name {
			continue
		}
		switch ref.Binding.Kind {
		case scope.BindResolved:
			// Target file: only count refs to the actual target decl
			// so unrelated same-name decls (sibling classes, builtin
			// resolves, etc.) don't inflate Resolved. Other files:
			// no target available — every name-matching Resolved ref
			// counts as a binding-confirmed candidate.
			if targetID == 0 || ref.Binding.Decl == targetID {
				audit.Resolved++
			}
		case scope.BindAmbiguous:
			addRefused(ref.Span, "ambiguous", ref.Binding.Reason)
		case scope.BindProbable:
			// Property-access refs (Ruby/Python obj.method,
			// Lua/Zig namespaced calls) typically have no Decl set —
			// strict treats them as not binding-confirmed.
			addRefused(ref.Span, "probable", ref.Binding.Reason)
		case scope.BindUnresolved:
			// Per-file scope binding is blind to cross-file resolution
			// in languages whose call sites go through the symbol-
			// index pipeline (Go same-package, C/C++ headers): a
			// legitimate cross-file ref looks BindUnresolved at this
			// layer even though the rename correctly handles it.
			// Counting it would over-refuse on the common cross-file
			// Go case. Strict gates only on Probable/Ambiguous —
			// genuine binding-gap signal is exposed via
			// 'refs-to --include-name-match'.
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
