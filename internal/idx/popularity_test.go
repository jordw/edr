package idx

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Popularity stack tests. The file-side (Write/Read round-trip +
// corruption handling) and the compute-side (per-symbol scoring)
// previously had zero direct coverage; they were exercised only
// implicitly via full-index rebuilds in integration tests.
//
// The scoring formula under test: for each symbol S defining name N
// in file F, score = sum over caller files C of {1 + log2(1 +
// InboundCount[C])} when C both (a) has a symbol that references N,
// and (b) imports F. Scenarios below build tiny graphs where the
// expected score is easy to compute by hand.

func TestWriteReadPopularity_RoundTrip(t *testing.T) {
	edrDir := t.TempDir()
	scores := []uint16{0, 1, 42, 65535}
	if err := WritePopularity(edrDir, scores); err != nil {
		t.Fatalf("WritePopularity: %v", err)
	}
	got := ReadPopularity(edrDir, len(scores))
	if !reflect.DeepEqual(got, scores) {
		t.Errorf("round-trip mismatch: got %v, want %v", got, scores)
	}
}

func TestWriteReadPopularity_Empty(t *testing.T) {
	edrDir := t.TempDir()
	if err := WritePopularity(edrDir, []uint16{}); err != nil {
		t.Fatalf("WritePopularity empty: %v", err)
	}
	// An empty slice round-trips, but ReadPopularity's nil-vs-empty
	// distinction matters less than the numSymbols gate. When the
	// caller says 0, we should get back something the caller can
	// safely range over.
	got := ReadPopularity(edrDir, 0)
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestReadPopularity_MissingFile(t *testing.T) {
	edrDir := t.TempDir()
	if got := ReadPopularity(edrDir, 5); got != nil {
		t.Errorf("missing file: got %v, want nil", got)
	}
}

func TestReadPopularity_BadMagic(t *testing.T) {
	edrDir := t.TempDir()
	// Valid header shape, wrong magic.
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint32(buf[0:], 0xDEADBEEF) // not popularityMagic
	binary.LittleEndian.PutUint32(buf[4:], 1)
	binary.LittleEndian.PutUint32(buf[8:], 0)
	if err := os.WriteFile(filepath.Join(edrDir, PopularityFile), buf, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadPopularity(edrDir, 0); got != nil {
		t.Errorf("bad magic: got %v, want nil", got)
	}
}

func TestReadPopularity_WrongVersion(t *testing.T) {
	edrDir := t.TempDir()
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint32(buf[0:], popularityMagic)
	binary.LittleEndian.PutUint32(buf[4:], 999) // unknown version
	binary.LittleEndian.PutUint32(buf[8:], 0)
	if err := os.WriteFile(filepath.Join(edrDir, PopularityFile), buf, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadPopularity(edrDir, 0); got != nil {
		t.Errorf("wrong version: got %v, want nil", got)
	}
}

func TestReadPopularity_SymbolCountMismatch(t *testing.T) {
	edrDir := t.TempDir()
	if err := WritePopularity(edrDir, []uint16{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	// Written with 3 symbols; caller asks for 5 → stale, return nil.
	if got := ReadPopularity(edrDir, 5); got != nil {
		t.Errorf("symbol count mismatch: got %v, want nil", got)
	}
}

func TestReadPopularity_TruncatedPayload(t *testing.T) {
	edrDir := t.TempDir()
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint32(buf[0:], popularityMagic)
	binary.LittleEndian.PutUint32(buf[4:], popularityVersion)
	binary.LittleEndian.PutUint32(buf[8:], 10) // claims 10 scores
	// But we only write the header — 20 bytes of payload are missing.
	if err := os.WriteFile(filepath.Join(edrDir, PopularityFile), buf, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadPopularity(edrDir, 10); got != nil {
		t.Errorf("truncated payload: got %v, want nil", got)
	}
}

func TestReadPopularity_ShorterThanHeader(t *testing.T) {
	edrDir := t.TempDir()
	// 4 bytes — shorter than the 12-byte header.
	if err := os.WriteFile(filepath.Join(edrDir, PopularityFile), []byte{1, 2, 3, 4}, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadPopularity(edrDir, 0); got != nil {
		t.Errorf("sub-header file: got %v, want nil", got)
	}
}

// --- ComputePopularity ---

// TestComputePopularity_NilGraphsReturnZeros: defensive guard — a
// ref graph or import graph missing should yield all-zero scores,
// not panic.
func TestComputePopularity_NilGraphsReturnZeros(t *testing.T) {
	symbols := []SymbolEntry{{Name: "Foo", FileID: 0}, {Name: "Bar", FileID: 1}}
	files := []FileEntry{{Path: "a.go"}, {Path: "b.go"}}
	if got := ComputePopularity(symbols, files, nil, nil); !allZeros(got) || len(got) != 2 {
		t.Errorf("nil graphs: got %v, want [0 0]", got)
	}
	ig := BuildImportGraph([]string{"a.go"}, nil)
	if got := ComputePopularity(symbols, files, ig, nil); !allZeros(got) {
		t.Errorf("nil refgraph: got %v, want all-zero", got)
	}
	rg := BuildRefGraphV2(2, [][]string{{}, {}})
	if got := ComputePopularity(symbols, files, nil, rg); !allZeros(got) {
		t.Errorf("nil import graph: got %v, want all-zero", got)
	}
}

func TestComputePopularity_EmptySymbols(t *testing.T) {
	ig := BuildImportGraph(nil, nil)
	rg := BuildRefGraphV2(0, nil)
	got := ComputePopularity(nil, nil, ig, rg)
	if len(got) != 0 {
		t.Errorf("empty symbols: got %v, want empty", got)
	}
}

// Smallest scoring scenario that exercises the intersection:
//
//   a.go: defines Foo        (symbol 0)
//   b.go: references Foo     (symbol 1)
//   b.go imports a.go
//
// Expected: Foo has exactly one caller (b.go) whose file imports a.go,
// b.go has InboundCount=0 so callerWeight = 1 + log2(1) = 1.
// Score(Foo) should be 1.
func TestComputePopularity_SingleImportingCaller(t *testing.T) {
	symbols := []SymbolEntry{
		{Name: "Foo", FileID: 0},
		{Name: "callSite", FileID: 1},
	}
	files := []FileEntry{{Path: "a.go"}, {Path: "b.go"}}
	ig := BuildImportGraph(
		[]string{"a.go", "b.go"},
		[][2]string{{"b.go", "a.go"}},
	)
	// symbol 0 (Foo) references nothing; symbol 1 (callSite) references "Foo".
	rg := BuildRefGraphV2(2, [][]string{
		{},      // Foo's body: no outgoing refs
		{"Foo"}, // callSite's body: calls Foo
	})
	got := ComputePopularity(symbols, files, ig, rg)
	if len(got) != 2 {
		t.Fatalf("got %d scores, want 2", len(got))
	}
	if got[0] != 1 {
		t.Errorf("score[Foo]=%d, want 1 (one importing caller, weight=1)", got[0])
	}
	if got[1] != 0 {
		t.Errorf("score[callSite]=%d, want 0 (nobody references it)", got[1])
	}
}

// Referencing without importing should NOT score. If b.go calls Foo
// but b.go doesn't import a.go, the intersection of callers ∩
// importers is empty — Foo scores zero even though there's a caller.
func TestComputePopularity_CallerMustImportDefFile(t *testing.T) {
	symbols := []SymbolEntry{
		{Name: "Foo", FileID: 0},
		{Name: "callSite", FileID: 1},
	}
	files := []FileEntry{{Path: "a.go"}, {Path: "b.go"}}
	ig := BuildImportGraph([]string{"a.go", "b.go"}, nil) // no edges
	rg := BuildRefGraphV2(2, [][]string{{}, {"Foo"}})
	got := ComputePopularity(symbols, files, ig, rg)
	if got[0] != 0 {
		t.Errorf("score[Foo]=%d, want 0 (caller b.go doesn't import a.go)", got[0])
	}
}

// Multiple definitions of the same name should score per-def based
// on which callers import which def's file.
//
//   a.go: defines Foo       (symbol 0)
//   b.go: defines Foo       (symbol 1) — same name, different file
//   c.go: calls Foo         (symbol 2)
//   c.go imports a.go only.
//
// Expected:
//   score[a.go/Foo] = 1 (c.go imports a.go and calls Foo)
//   score[b.go/Foo] = 0 (c.go calls Foo but doesn't import b.go)
func TestComputePopularity_MultipleDefsScoredPerFile(t *testing.T) {
	symbols := []SymbolEntry{
		{Name: "Foo", FileID: 0},
		{Name: "Foo", FileID: 1},
		{Name: "caller", FileID: 2},
	}
	files := []FileEntry{{Path: "a.go"}, {Path: "b.go"}, {Path: "c.go"}}
	ig := BuildImportGraph(
		[]string{"a.go", "b.go", "c.go"},
		[][2]string{{"c.go", "a.go"}},
	)
	rg := BuildRefGraphV2(3, [][]string{
		{},      // a/Foo
		{},      // b/Foo
		{"Foo"}, // c/caller references Foo (ambiguous — resolution elsewhere)
	})
	got := ComputePopularity(symbols, files, ig, rg)
	if got[0] != 1 {
		t.Errorf("score[a.go/Foo]=%d, want 1 (importing caller)", got[0])
	}
	if got[1] != 0 {
		t.Errorf("score[b.go/Foo]=%d, want 0 (caller doesn't import b.go)", got[1])
	}
}

// Caller-weight math: heavily-imported caller files should boost the
// definition's score. Formula: weight = 1 + log2(1 + inbound).
//
//   lib.go:     defines Foo
//   mock.go:    defines Foo (duplicate to verify per-def math)
//   call1.go:   inbound=0 → weight 1
//   call2.go:   inbound=3 → weight 1 + log2(4) = 3
//   Both call1 and call2 reference Foo; both import lib.go.
//   Three files import call2 (bumping its inbound to 3).
//   Nothing imports mock.go, so mock.go/Foo scores 0.
//
// Expected score[lib.go/Foo] = 1 + 3 = 4
//           score[mock.go/Foo] = 0
func TestComputePopularity_WeightsFromInboundCount(t *testing.T) {
	symbols := []SymbolEntry{
		{Name: "Foo", FileID: 0},    // lib.go
		{Name: "Foo", FileID: 1},    // mock.go
		{Name: "call1", FileID: 2},  // call1.go
		{Name: "call2", FileID: 3},  // call2.go
		{Name: "imp1", FileID: 4},   // importers of call2 — contribute inbound
		{Name: "imp2", FileID: 5},
		{Name: "imp3", FileID: 6},
	}
	files := []FileEntry{
		{Path: "lib.go"},
		{Path: "mock.go"},
		{Path: "call1.go"},
		{Path: "call2.go"},
		{Path: "imp1.go"},
		{Path: "imp2.go"},
		{Path: "imp3.go"},
	}
	ig := BuildImportGraph(
		[]string{"lib.go", "mock.go", "call1.go", "call2.go", "imp1.go", "imp2.go", "imp3.go"},
		[][2]string{
			{"call1.go", "lib.go"}, // call1 imports lib
			{"call2.go", "lib.go"}, // call2 imports lib
			{"imp1.go", "call2.go"},
			{"imp2.go", "call2.go"},
			{"imp3.go", "call2.go"},
		},
	)
	rg := BuildRefGraphV2(7, [][]string{
		{},      // lib/Foo
		{},      // mock/Foo
		{"Foo"}, // call1 references Foo
		{"Foo"}, // call2 references Foo
		{},      // imp1
		{},      // imp2
		{},      // imp3
	})
	got := ComputePopularity(symbols, files, ig, rg)

	// Expected: call1 weight=1, call2 weight=1+log2(4)=3 → total 4.
	wantLib := uint16(math.Floor(1 + (1 + math.Log2(4))))
	if got[0] != wantLib {
		t.Errorf("score[lib.go/Foo]=%d, want %d (call1 weight 1 + call2 weight 3)", got[0], wantLib)
	}
	if got[1] != 0 {
		t.Errorf("score[mock.go/Foo]=%d, want 0 (nothing imports mock.go)", got[1])
	}
}

// Covers the loop-direction branch where callerGraphFiles is smaller
// than importers. Optimization path: when few callers reference a
// name but many files import the def's file, the code iterates
// callers rather than importers. The *other* branch is exercised by
// TestComputePopularity_WeightsFromInboundCount (equal sizes → else
// branch).
func TestComputePopularity_FewerCallersThanImporters(t *testing.T) {
	symbols := []SymbolEntry{
		{Name: "Foo", FileID: 0},
		{Name: "only_caller", FileID: 1},
	}
	files := []FileEntry{
		{Path: "a.go"}, {Path: "b.go"}, {Path: "c.go"}, {Path: "d.go"},
	}
	ig := BuildImportGraph(
		[]string{"a.go", "b.go", "c.go", "d.go"},
		[][2]string{
			{"b.go", "a.go"}, // b imports a, references Foo
			{"c.go", "a.go"}, // c imports a but doesn't reference Foo
			{"d.go", "a.go"}, // d imports a but doesn't reference Foo
		},
	)
	// Only b.go references Foo. Importers of a.go = 3 files; callers = 1.
	rg := BuildRefGraphV2(2, [][]string{{}, {"Foo"}})
	got := ComputePopularity(symbols, files, ig, rg)
	// Expected: callerGraphFiles = {b.go}, importers of a.go = {b,c,d}.
	// Intersection = {b.go} with weight 1. Score = 1.
	if got[0] != 1 {
		t.Errorf("score[Foo]=%d, want 1 (only b.go both calls and imports)", got[0])
	}
}

// --- helpers ---

func allZeros(s []uint16) bool {
	for _, v := range s {
		if v != 0 {
			return false
		}
	}
	return true
}
