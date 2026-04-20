package rename

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/ts"
)

// spanOfNthOccurrence returns the scope.Span covering the n-th (0-indexed)
// occurrence of needle as a standalone identifier (bounded by non-ident
// chars) in src.
func spanOfNthOccurrence(t *testing.T, src []byte, needle string, n int) scope.Span {
	t.Helper()
	seen := 0
	for i := 0; i+len(needle) <= len(src); i++ {
		if !bytes.HasPrefix(src[i:], []byte(needle)) {
			continue
		}
		if i > 0 && isIdentCont(src[i-1]) {
			continue
		}
		if i+len(needle) < len(src) && isIdentCont(src[i+len(needle)]) {
			continue
		}
		if seen == n {
			return scope.Span{StartByte: uint32(i), EndByte: uint32(i + len(needle))}
		}
		seen++
	}
	t.Fatalf("occurrence %d of %q not found in src", n, needle)
	return scope.Span{}
}

func planExpectReady(t *testing.T, src []byte, span scope.Span, newName string) *Ready {
	t.Helper()
	res := ts.Parse("f.ts", src)
	ready, refused := Plan(res, src, span, newName)
	if refused != nil {
		t.Fatalf("expected Ready, got Refused: %+v", refused)
	}
	if ready == nil {
		t.Fatalf("expected Ready, got nil/nil plan")
	}
	return ready
}

func planExpectRefused(t *testing.T, src []byte, span scope.Span, newName string, wantReason string) *Refused {
	t.Helper()
	res := ts.Parse("f.ts", src)
	ready, refused := Plan(res, src, span, newName)
	if ready != nil {
		t.Fatalf("expected Refused(%s), got Ready with %d edits", wantReason, len(ready.Edits))
	}
	if refused == nil {
		t.Fatalf("expected Refused(%s), got nil/nil plan", wantReason)
	}
	if refused.Reason != wantReason {
		t.Fatalf("Refused.Reason = %q, want %q (detail: %s)", refused.Reason, wantReason, refused.Detail)
	}
	return refused
}

func TestPlan_BasicLocalRename(t *testing.T) {
	src := []byte(`const x = 42
const y = x + 1
const z = x * 2
`)
	span := spanOfNthOccurrence(t, src, "x", 0)
	ready := planExpectReady(t, src, span, "renamed")

	if ready.OldName != "x" || ready.NewName != "renamed" {
		t.Fatalf("names wrong: %+v", ready)
	}
	if len(ready.Edits) != 3 {
		t.Fatalf("want 3 edits (decl + 2 refs), got %d: %+v", len(ready.Edits), ready.Edits)
	}

	newSrc := ready.Apply()
	wantStr := `const renamed = 42
const y = renamed + 1
const z = renamed * 2
`
	if string(newSrc) != wantStr {
		t.Fatalf("Apply produced unexpected output.\nwant:\n%s\ngot:\n%s", wantStr, newSrc)
	}
}

func TestPlan_RefSpanResolvesToDecl(t *testing.T) {
	src := []byte(`function foo() { return 1 }
function bar() { return foo() + foo() }
`)
	// Span points at the first *reference* to foo, not the decl.
	span := spanOfNthOccurrence(t, src, "foo", 1)
	ready := planExpectReady(t, src, span, "foo2")
	if ready.OldName != "foo" {
		t.Fatalf("OldName = %q, want foo", ready.OldName)
	}
	if len(ready.Edits) != 3 {
		t.Fatalf("want 3 edits (decl + 2 refs), got %d", len(ready.Edits))
	}
	out := string(ready.Apply())
	// Exactly three references to the renamed identifier, and none to the old name.
	wantOccurrences := `function foo2() { return 1 }
function bar() { return foo2() + foo2() }
`
	if out != wantOccurrences {
		t.Fatalf("unexpected output:\nwant:\n%s\ngot:\n%s", wantOccurrences, out)
	}
	if strings.Count(out, "foo2") != 3 {
		t.Fatalf("want 3 foo2 occurrences, got %d:\n%s", strings.Count(out, "foo2"), out)
	}
}

func TestPlan_Refused_NoSymbolAtSpan(t *testing.T) {
	src := []byte(`const xyz = 1
// a comment mentioning nothing
`)
	// Span is inside the comment — no decl or ref covers it.
	pos := bytes.Index(src, []byte("mentioning"))
	span := scope.Span{StartByte: uint32(pos), EndByte: uint32(pos + len("mentioning"))}
	planExpectRefused(t, src, span, "renamed", ReasonNoSymbolAtSpan)
}

func TestPlan_Refused_MixedConfidence(t *testing.T) {
	// The TS builder's naturally-occurring non-Resolved refs (property
	// access, missing imports) don't point back at the target DeclID,
	// so they don't surface via RefsToDecl. To assert the refusal
	// contract independently of builder behavior, construct a scope
	// result with a known Ambiguous ref to the target. If the builder
	// later starts emitting real Ambiguous bindings to Decl IDs, a
	// dogfood test will catch that — this test owns the refusal rule.
	const src = "const x = 1\nconst y = x\n"
	srcBytes := []byte(src)
	declSpan := scope.Span{StartByte: 6, EndByte: 7}  // `x` in `const x`
	ref1Span := scope.Span{StartByte: 22, EndByte: 23} // `x` in `= x`
	res := &scope.Result{
		File: "f.ts",
		Scopes: []scope.Scope{
			{ID: 1, Parent: 0, Kind: scope.ScopeFile, Span: scope.Span{StartByte: 0, EndByte: uint32(len(src))}},
		},
		Decls: []scope.Decl{
			{ID: 42, Name: "x", Namespace: scope.NSValue, Kind: scope.KindConst, Scope: 1, File: "f.ts", Span: declSpan},
		},
		Refs: []scope.Ref{
			{File: "f.ts", Span: ref1Span, Name: "x", Namespace: scope.NSValue, Scope: 1,
				Binding: scope.RefBinding{Kind: scope.BindAmbiguous, Candidates: []scope.DeclID{42, 99}, Reason: "structural_method_match"}},
		},
	}
	ready, refused := Plan(res, srcBytes, declSpan, "z")
	if ready != nil {
		t.Fatalf("expected Refused(mixed_confidence), got Ready")
	}
	if refused == nil || refused.Reason != ReasonMixedConfidence {
		t.Fatalf("Refused = %v, want reason %s", refused, ReasonMixedConfidence)
	}
}

// TestPlan_Refused_StaleIndex: when the decl span in the scope result
// points at bytes that spell a different name than Decl.Name, the
// index has drifted from src. Refuse rather than apply a corrupt
// rewrite.
func TestPlan_Refused_StaleIndex(t *testing.T) {
	// Src contains `foo` at offset 6; scope result claims the decl is
	// named "xyz" with span [6:9). Plan must refuse.
	src := []byte("const foo = 1\n")
	declSpan := scope.Span{StartByte: 6, EndByte: 9}
	res := &scope.Result{
		File: "f.ts",
		Scopes: []scope.Scope{
			{ID: 1, Parent: 0, Kind: scope.ScopeFile, Span: scope.Span{StartByte: 0, EndByte: uint32(len(src))}},
		},
		Decls: []scope.Decl{
			{ID: 1, Name: "xyz", Namespace: scope.NSValue, Kind: scope.KindConst, Scope: 1, File: "f.ts", Span: declSpan},
		},
	}
	ready, refused := Plan(res, src, declSpan, "bar")
	if ready != nil {
		t.Fatalf("expected Refused(stale_index), got Ready")
	}
	if refused == nil || refused.Reason != ReasonStaleIndex {
		t.Fatalf("Refused = %v, want reason %s", refused, ReasonStaleIndex)
	}
}

// TestPlan_Refused_OverlappingEdits: two ref spans overlap, and each
// still reads the target name (so the stale_index guard passes first).
// This only works when the target name has a non-trivial self-overlap
// period — e.g. "aa" inside "aaa". The TS builder won't naturally
// produce this shape, but the refusal must hold if it ever does.
func TestPlan_Refused_OverlappingEdits(t *testing.T) {
	//        0123456789012345
	// src = "const aa = aaa\n"
	// decl `aa`  at [6:8)   -> "aa"
	// refA       at [11:13) -> "aa" (first two chars of "aaa")
	// refB       at [12:14) -> "aa" (last two chars of "aaa")   -- overlaps refA
	src := []byte("const aa = aaa\n")
	declSpan := scope.Span{StartByte: 6, EndByte: 8}
	refA := scope.Span{StartByte: 11, EndByte: 13}
	refB := scope.Span{StartByte: 12, EndByte: 14}
	res := &scope.Result{
		File: "f.ts",
		Scopes: []scope.Scope{
			{ID: 1, Parent: 0, Kind: scope.ScopeFile, Span: scope.Span{StartByte: 0, EndByte: uint32(len(src))}},
		},
		Decls: []scope.Decl{
			{ID: 7, Name: "aa", Namespace: scope.NSValue, Kind: scope.KindConst, Scope: 1, File: "f.ts", Span: declSpan},
		},
		Refs: []scope.Ref{
			{File: "f.ts", Span: refA, Name: "aa", Namespace: scope.NSValue, Scope: 1,
				Binding: scope.RefBinding{Kind: scope.BindResolved, Decl: 7, Reason: "direct_scope"}},
			{File: "f.ts", Span: refB, Name: "aa", Namespace: scope.NSValue, Scope: 1,
				Binding: scope.RefBinding{Kind: scope.BindResolved, Decl: 7, Reason: "direct_scope"}},
		},
	}
	ready, refused := Plan(res, src, declSpan, "bb")
	if ready != nil {
		t.Fatalf("expected Refused(overlapping_edits), got Ready")
	}
	if refused == nil || refused.Reason != ReasonOverlappingEdits {
		t.Fatalf("Refused = %v, want reason %s", refused, ReasonOverlappingEdits)
	}
}

func TestPlan_Refused_Collision_SameScope(t *testing.T) {
	src := []byte(`const x = 1
const y = 2
const z = x + y
`)
	span := spanOfNthOccurrence(t, src, "x", 0)
	planExpectRefused(t, src, span, "y", ReasonCollision)
}

func TestPlan_Refused_Collision_InnerShadow(t *testing.T) {
	// Renaming outer `a` -> `b` would be shadowed by the inner `let b`.
	src := []byte(`function f() {
  const a = 1
  if (true) {
    let b = 2
    return a + b
  }
  return a
}
`)
	span := spanOfNthOccurrence(t, src, "a", 0)
	planExpectRefused(t, src, span, "b", ReasonCollision)
}

func TestPlan_Refused_Collision_OuterShadow(t *testing.T) {
	// Renaming inner local -> name that's already a decl in an outer scope.
	src := []byte(`const outer = 1
function f() {
  const inner = 2
  return inner + outer
}
`)
	span := spanOfNthOccurrence(t, src, "inner", 0)
	planExpectRefused(t, src, span, "outer", ReasonCollision)
}

func TestPlan_Refused_InvalidNewName_Reserved(t *testing.T) {
	src := []byte(`const x = 1
`)
	span := spanOfNthOccurrence(t, src, "x", 0)
	planExpectRefused(t, src, span, "class", ReasonInvalidNewName)
}

func TestPlan_Refused_InvalidNewName_Syntax(t *testing.T) {
	src := []byte(`const x = 1
`)
	span := spanOfNthOccurrence(t, src, "x", 0)
	planExpectRefused(t, src, span, "3bad", ReasonInvalidNewName)
}

func TestPlan_Refused_NoChange(t *testing.T) {
	src := []byte(`const x = 1
`)
	span := spanOfNthOccurrence(t, src, "x", 0)
	planExpectRefused(t, src, span, "x", ReasonNoChange)
}

// Round-trip: rename X -> Y -> X must be byte-identical to the original.
// A pure function of scope results, so this exercises both the TS
// builder (parse after first rename) and the rename package.
func TestPlan_RoundTrip(t *testing.T) {
	cases := []struct {
		name        string
		src         string
		old         string
		tmp         string
		occurrence  int // which textual occurrence of `old` to target
	}{
		{
			name: "local_variable",
			src:  "const foo = 1\nconst bar = foo + 2\n",
			old:  "foo",
			tmp:  "renamedFoo",
		},
		{
			name: "function",
			src:  "function doThing(x) { return x + 1 }\nconst r = doThing(3)\n",
			old:  "doThing",
			tmp:  "doOther",
		},
		{
			name: "class_method",
			src:  "class C {\n  greet() { return 'hi' }\n  run() { return this.greet() }\n}\n",
			old:  "greet",
			tmp:  "salute",
		},
		{
			name: "imported_name_local_side",
			src:  "import { api } from './mod'\nconst result = api()\n",
			old:  "api",
			tmp:  "client",
		},
		{
			// Name shadowed in a nested scope: rename the INNER
			// binding (and its local refs) to a name unique at every
			// scope level, then back. The outer `x` decl and its ref
			// on the last line must stay untouched in both directions.
			name:       "shadowed_name",
			src:        "const x = 1\nfunction f() {\n  const x = 2\n  return x + 1\n}\nconst y = x\n",
			old:        "x",
			tmp:        "innerOnly",
			occurrence: 1, // inner `const x = 2`
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srcOrig := []byte(tc.src)

			// First rename: old -> tmp. Target the Nth occurrence.
			res1 := ts.Parse("f.ts", srcOrig)
			span1 := spanOfNthOccurrence(t, srcOrig, tc.old, tc.occurrence)
			ready1, refused1 := Plan(res1, srcOrig, span1, tc.tmp)
			if refused1 != nil {
				t.Fatalf("first rename refused: %+v", refused1)
			}
			src2 := ready1.Apply()

			// Second rename: tmp -> old. Target the first occurrence in
			// the new source. Re-parse — must not mutate res1.
			res2 := ts.Parse("f.ts", src2)
			span2 := spanOfNthOccurrence(t, src2, tc.tmp, 0)
			ready2, refused2 := Plan(res2, src2, span2, tc.old)
			if refused2 != nil {
				t.Fatalf("second rename refused: %+v (src2=%s)", refused2, src2)
			}
			src3 := ready2.Apply()

			if !bytes.Equal(src3, srcOrig) {
				t.Fatalf("round-trip not byte-identical.\noriginal:\n%s\nintermediate:\n%s\nfinal:\n%s",
					srcOrig, src2, src3)
			}
		})
	}
}

// TestCompileTimeSafety documents — by construction of the calls that
// DO compile — the invariant that Apply is defined only on *Ready.
// Plan's (*Ready, *Refused) return shape makes the refusal visible to
// every caller; attempting `Plan(...).Apply()` fails to compile because
// Plan returns two values. Attempting `(*Refused).Apply()` fails
// because no such method exists. Those negative checks live out-of-band;
// including them here would break the build.
func TestCompileTimeSafety(t *testing.T) {
	var r *Ready
	if r != nil {
		_ = r.Apply()
	}
	_ = &Refused{Reason: ReasonCollision}
}
