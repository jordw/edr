package idx

import (
	"reflect"
	"sort"
	"testing"
)

// Reference graph read-path tests. BuildRefGraphV2 and WriteRefGraph
// are covered by other tests (patch_invalidation_test.go seeds a real
// graph; build.go paths exercise construction at index time). The
// consumer-side Callers/CallersByName/Callees/CalleeNames methods
// that power `edr explore` and callers/callees navigation had no
// direct tests — this file closes that gap.
//
// Scenario used throughout: a 4-symbol graph with a simple fan-in/
// fan-out pattern so every edge shape is exercised:
//
//	A → B, C     // A references two names
//	B → C        // shared fan-in target
//	C → A        // back-edge
//	D            // zero references, zero referrers
//
// Expected reverse index:
//
//	hash("A") ← C
//	hash("B") ← A
//	hash("C") ← A, B   (two callers — the case where InvSymIDs is >1)
//	hash("D") ← —       (nobody references D)
func buildScenarioGraph(t *testing.T) *RefGraphData {
	t.Helper()
	return BuildRefGraphV2(4, [][]string{
		{"B", "C"}, // symbol 0: A
		{"C"},      // symbol 1: B
		{"A"},      // symbol 2: C
		{},         // symbol 3: D
	})
}

func TestCalleeNames_ReturnsReferencedHashes(t *testing.T) {
	g := buildScenarioGraph(t)

	wantA := []uint64{NameHash("B"), NameHash("C")}
	got := g.CalleeNames(0)
	// Forward names are in insertion order (not sorted). Compare as
	// a set so the test isn't coupled to BuildRefGraphV2 ordering.
	if !sameHashSet(got, wantA) {
		t.Errorf("CalleeNames(A) = %v, want set %v", got, wantA)
	}

	if got := g.CalleeNames(1); !sameHashSet(got, []uint64{NameHash("C")}) {
		t.Errorf("CalleeNames(B) = %v, want [hash(C)]", got)
	}

	if got := g.CalleeNames(2); !sameHashSet(got, []uint64{NameHash("A")}) {
		t.Errorf("CalleeNames(C) = %v, want [hash(A)]", got)
	}

	// D references nothing — start == end, expect nil.
	if got := g.CalleeNames(3); got != nil {
		t.Errorf("CalleeNames(D) = %v, want nil (symbol has no refs)", got)
	}
}

func TestCalleeNames_NilAndOutOfRange(t *testing.T) {
	var nilGraph *RefGraphData
	if got := nilGraph.CalleeNames(0); got != nil {
		t.Errorf("nil graph: CalleeNames = %v, want nil", got)
	}

	g := buildScenarioGraph(t)
	// callerID equal to NumSymbols is out of range (valid IDs are 0..N-1).
	if got := g.CalleeNames(4); got != nil {
		t.Errorf("CalleeNames(NumSymbols) = %v, want nil (out of range)", got)
	}
	if got := g.CalleeNames(999); got != nil {
		t.Errorf("CalleeNames(999) = %v, want nil", got)
	}
}

func TestCallersByName_SingleAndMultipleCallers(t *testing.T) {
	g := buildScenarioGraph(t)

	// hash("A") is referenced only by C.
	if got := g.CallersByName(NameHash("A")); !sameUint32Set(got, []uint32{2}) {
		t.Errorf("CallersByName(A) = %v, want [2]", got)
	}
	// hash("B") is referenced only by A.
	if got := g.CallersByName(NameHash("B")); !sameUint32Set(got, []uint32{0}) {
		t.Errorf("CallersByName(B) = %v, want [0]", got)
	}
	// hash("C") is referenced by both A and B — multi-caller case.
	if got := g.CallersByName(NameHash("C")); !sameUint32Set(got, []uint32{0, 1}) {
		t.Errorf("CallersByName(C) = %v, want [0, 1]", got)
	}
}

func TestCallersByName_MissingNameReturnsNil(t *testing.T) {
	g := buildScenarioGraph(t)
	// D is defined but nobody references it — its hash shouldn't be in
	// InvEntries at all.
	if got := g.CallersByName(NameHash("D")); got != nil {
		t.Errorf("CallersByName(D) = %v, want nil (no references to D)", got)
	}
	// Random hash that was never inserted.
	if got := g.CallersByName(NameHash("NoSuchSymbol")); got != nil {
		t.Errorf("CallersByName(unknown) = %v, want nil", got)
	}
}

func TestCallersByName_NilAndEmpty(t *testing.T) {
	var nilGraph *RefGraphData
	if got := nilGraph.CallersByName(NameHash("anything")); got != nil {
		t.Errorf("nil graph: CallersByName = %v, want nil", got)
	}
	empty := &RefGraphData{NumSymbols: 0}
	if got := empty.CallersByName(NameHash("anything")); got != nil {
		t.Errorf("empty graph: CallersByName = %v, want nil", got)
	}
}

// Binary search correctness: InvEntries must be sorted by NameHash for
// CallersByName's sort.Search to work. This test builds a graph with
// a large-ish symbol set so the sorted property is exercised.
func TestCallersByName_BinarySearchAcrossManyEntries(t *testing.T) {
	names := []string{"alpha", "beta", "gamma", "delta", "epsilon",
		"zeta", "eta", "theta", "iota", "kappa", "lambda", "mu"}
	// Each symbol references one distinct name (the next in the ring).
	n := len(names)
	idents := make([][]string, n)
	for i := range idents {
		idents[i] = []string{names[(i+1)%n]}
	}
	g := BuildRefGraphV2(uint32(n), idents)

	// Assert the inverted index is sorted by NameHash — precondition
	// for CallersByName's sort.Search.
	if !sort.SliceIsSorted(g.InvEntries, func(i, j int) bool {
		return g.InvEntries[i].NameHash < g.InvEntries[j].NameHash
	}) {
		t.Fatalf("InvEntries not sorted — CallersByName binary search would be wrong")
	}

	// Each name should have exactly one caller (the predecessor in the ring).
	for i, name := range names {
		wantCaller := uint32((i - 1 + n) % n)
		got := g.CallersByName(NameHash(name))
		if !sameUint32Set(got, []uint32{wantCaller}) {
			t.Errorf("CallersByName(%q) = %v, want [%d]", name, got, wantCaller)
		}
	}
}

// Corrupted-graph defensive check: if InvEntries points beyond
// InvSymIDs (bit-flipped on disk, truncated read, etc.), CallersByName
// must return nil rather than panic with a slice out-of-bounds. Hand-
// construct a malformed graph to hit the defensive branch.
func TestCallersByName_CorruptedOffsetReturnsNil(t *testing.T) {
	nameHash := NameHash("X")
	g := &RefGraphData{
		NumSymbols: 1,
		InvEntries: []InvEntry{
			{NameHash: nameHash, Offset: 0, Count: 100}, // claims 100 IDs
		},
		InvSymIDs: []uint32{0}, // only one actually present
	}
	if got := g.CallersByName(nameHash); got != nil {
		t.Errorf("corrupted graph: CallersByName = %v, want nil (bounds check)", got)
	}
}

// --- helpers ---

// sameHashSet reports whether a and b contain the same set of uint64
// values, ignoring order. Forward names and caller IDs are stored in
// insertion / appearance order; tests care about membership, not
// position.
func sameHashSet(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	aSorted := append([]uint64(nil), a...)
	bSorted := append([]uint64(nil), b...)
	sort.Slice(aSorted, func(i, j int) bool { return aSorted[i] < aSorted[j] })
	sort.Slice(bSorted, func(i, j int) bool { return bSorted[i] < bSorted[j] })
	return reflect.DeepEqual(aSorted, bSorted)
}

func sameUint32Set(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	aSorted := append([]uint32(nil), a...)
	bSorted := append([]uint32(nil), b...)
	sort.Slice(aSorted, func(i, j int) bool { return aSorted[i] < aSorted[j] })
	sort.Slice(bSorted, func(i, j int) bool { return bSorted[i] < bSorted[j] })
	return reflect.DeepEqual(aSorted, bSorted)
}
