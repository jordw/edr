package idx

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// buildSymbolIndexData constructs a fully-populated IndexData with the three
// symbol-index fields QuerySymbolsBy* use. Used by the in-memory query tests
// below so we don't have to round-trip through disk.
func buildSymbolIndexData(files []FileEntry, symbols []SymbolEntry) *IndexData {
	namePostings, namePosts := BuildNamePostings(symbols)
	return &IndexData{
		Header: Header{
			NumFiles:    uint32(len(files)),
			NumSymbols:  uint32(len(symbols)),
			NumNameKeys: uint32(len(namePosts)),
		},
		Files:        files,
		Symbols:      symbols,
		NamePosts:    namePosts,
		NamePostings: namePostings,
	}
}

func TestSymbolIndexRoundTrip(t *testing.T) {
	// Build a minimal index with symbols
	d := &IndexData{
		Header: Header{
			NumFiles: 1, NumTrigrams: 0, GitMtime: 12345,
		},
		Files: []FileEntry{{Path: "main.go", Mtime: 100, Size: 50}},
		Symbols: []SymbolEntry{
			{FileID: 0, Name: "hello", Kind: KindFunction, StartLine: 3, EndLine: 5, StartByte: 15, EndByte: 45},
			{FileID: 0, Name: "Config", Kind: KindStruct, StartLine: 7, EndLine: 10, StartByte: 47, EndByte: 80},
			{FileID: 0, Name: "hello", Kind: KindFunction, StartLine: 20, EndLine: 22, StartByte: 100, EndByte: 130},
		},
	}

	// Build name postings
	npData, npEntries := BuildNamePostings(d.Symbols)
	d.NamePostings = npData
	d.NamePosts = npEntries
	d.Header.NumSymbols = uint32(len(d.Symbols))
	d.Header.NumNameKeys = uint32(len(npEntries))

	// Marshal
	data := d.Marshal()

	// Write to temp dir
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, MainFile), data, 0600); err != nil {
		t.Fatal(err)
	}

	// Read header
	h, err := ReadHeader(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h.Version != 3 {
		t.Errorf("version = %d, want 3", h.Version)
	}
	if h.NumSymbols != 3 {
		t.Errorf("numSymbols = %d, want 3", h.NumSymbols)
	}

	// Unmarshal
	d2, err := Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(d2.Symbols) != 3 {
		t.Fatalf("symbols = %d, want 3", len(d2.Symbols))
	}
	if d2.Symbols[0].Name != "hello" || d2.Symbols[0].Kind != KindFunction {
		t.Errorf("symbol 0 = %v", d2.Symbols[0])
	}
	if d2.Symbols[1].Name != "Config" || d2.Symbols[1].Kind != KindStruct {
		t.Errorf("symbol 1 = %v", d2.Symbols[1])
	}

	// Query by name
	results := QuerySymbolsByName(d2, "hello")
	if len(results) != 2 {
		t.Fatalf("query 'hello' = %d results, want 2", len(results))
	}
	results = QuerySymbolsByName(d2, "Config")
	if len(results) != 1 {
		t.Fatalf("query 'Config' = %d results, want 1", len(results))
	}
	results = QuerySymbolsByName(d2, "nonexistent")
	if len(results) != 0 {
		t.Fatalf("query 'nonexistent' = %d results, want 0", len(results))
	}
}

// TestQuerySymbolsByHash_FindsMatchesByPrecomputedHash verifies that hash
// lookup returns every symbol whose name hashes to the given value.
// Unlike QuerySymbolsByName this skips the exact-name check, which is the
// whole point — callers that already have the hash shouldn't pay for the
// string compare.
func TestQuerySymbolsByHash_FindsMatchesByPrecomputedHash(t *testing.T) {
	d := buildSymbolIndexData(
		[]FileEntry{{Path: "a.go", Size: 100}},
		[]SymbolEntry{
			{FileID: 0, Name: "Foo", Kind: KindFunction, StartLine: 1, EndLine: 2, StartByte: 0, EndByte: 10},
			{FileID: 0, Name: "Foo", Kind: KindFunction, StartLine: 5, EndLine: 7, StartByte: 20, EndByte: 30},
			{FileID: 0, Name: "Bar", Kind: KindFunction, StartLine: 10, EndLine: 11, StartByte: 40, EndByte: 50},
		},
	)
	results := QuerySymbolsByHash(d, NameHash("Foo"))
	if len(results) != 2 {
		t.Fatalf("QuerySymbolsByHash(Foo) = %d results, want 2", len(results))
	}
	for _, r := range results {
		if r.Name != "Foo" {
			t.Errorf("got entry with Name=%q, want Foo", r.Name)
		}
	}
	if got := QuerySymbolsByHash(d, NameHash("Bar")); len(got) != 1 || got[0].Name != "Bar" {
		t.Errorf("QuerySymbolsByHash(Bar) = %+v, want one Bar", got)
	}
	if got := QuerySymbolsByHash(d, NameHash("nope")); len(got) != 0 {
		t.Errorf("QuerySymbolsByHash(missing) = %+v, want empty", got)
	}
}

// TestQuerySymbolsByHash_EmptyIndexReturnsNil covers the fast-path for an
// index with no symbol data.
func TestQuerySymbolsByHash_EmptyIndexReturnsNil(t *testing.T) {
	d := &IndexData{}
	if got := QuerySymbolsByHash(d, NameHash("anything")); got != nil {
		t.Errorf("empty index should return nil, got %+v", got)
	}
}

// TestQuerySymbolsWithIDs_PairsSymbolWithIndexPosition verifies the IDs
// returned point at the correct positions in d.Symbols (needed so callers
// can look up popularity scores).
func TestQuerySymbolsWithIDs_PairsSymbolWithIndexPosition(t *testing.T) {
	syms := []SymbolEntry{
		{FileID: 0, Name: "Alpha", Kind: KindFunction, StartLine: 1, EndByte: 10},
		{FileID: 0, Name: "Beta", Kind: KindFunction, StartLine: 2, EndByte: 20},
		{FileID: 0, Name: "Alpha", Kind: KindFunction, StartLine: 3, EndByte: 30},
	}
	d := buildSymbolIndexData(
		[]FileEntry{{Path: "a.go", Size: 100}},
		syms,
	)
	got := QuerySymbolsWithIDs(d, "Alpha")
	if len(got) != 2 {
		t.Fatalf("QuerySymbolsWithIDs(Alpha) = %d, want 2", len(got))
	}
	for _, e := range got {
		if int(e.IndexID) >= len(d.Symbols) {
			t.Fatalf("IndexID %d out of range (len=%d)", e.IndexID, len(d.Symbols))
		}
		if d.Symbols[e.IndexID].Name != "Alpha" {
			t.Errorf("IndexID %d points at %q, want Alpha", e.IndexID, d.Symbols[e.IndexID].Name)
		}
		if e.SymbolEntry.StartLine != d.Symbols[e.IndexID].StartLine {
			t.Errorf("embedded SymbolEntry doesn't match Symbols[%d]", e.IndexID)
		}
	}
	ids := []uint32{got[0].IndexID, got[1].IndexID}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if ids[0] != 0 || ids[1] != 2 {
		t.Errorf("IndexIDs = %v, want [0 2]", ids)
	}
}

func TestQuerySymbolsWithIDs_MissingNameAndEmptyIndex(t *testing.T) {
	d := buildSymbolIndexData(
		[]FileEntry{{Path: "a.go"}},
		[]SymbolEntry{{FileID: 0, Name: "Only"}},
	)
	if got := QuerySymbolsWithIDs(d, "Nope"); got != nil {
		t.Errorf("missing name should return nil, got %+v", got)
	}
	empty := &IndexData{}
	if got := QuerySymbolsWithIDs(empty, "Only"); got != nil {
		t.Errorf("empty index should return nil, got %+v", got)
	}
}

// TestAllIndexedSymbols_ReturnsUnderlyingSlice documents the passthrough
// contract — the function is a trivial accessor for callers who want the
// whole symbol table without reaching into IndexData directly.
func TestAllIndexedSymbols_ReturnsUnderlyingSlice(t *testing.T) {
	syms := []SymbolEntry{
		{Name: "A", Kind: KindFunction},
		{Name: "B", Kind: KindStruct},
	}
	d := &IndexData{Symbols: syms}
	got := AllIndexedSymbols(d)
	if len(got) != 2 || got[0].Name != "A" || got[1].Name != "B" {
		t.Errorf("AllIndexedSymbols = %+v, want [A B]", got)
	}
	// nil Symbols stays nil.
	if got := AllIndexedSymbols(&IndexData{}); got != nil {
		t.Errorf("empty index should pass through nil, got %+v", got)
	}
}

// TestHasSymbolIndex_TrueOnlyWhenV3WithSymbols covers the three branches of
// the header check: missing file, pre-v3 index, and v3-with-symbols.
func TestHasSymbolIndex_TrueOnlyWhenV3WithSymbols(t *testing.T) {
	// Missing index dir → false (ReadHeader error).
	empty := t.TempDir()
	if HasSymbolIndex(empty) {
		t.Error("HasSymbolIndex on empty dir = true, want false")
	}

	// v3 index with symbols → true.
	withSyms := t.TempDir()
	seedIndex(t, withSyms,
		[]FileEntry{{Path: "a.go", Size: 10}},
		[]SymbolEntry{{FileID: 0, Name: "Foo", Kind: KindFunction, EndByte: 5}},
	)
	if !HasSymbolIndex(withSyms) {
		t.Error("HasSymbolIndex on v3 index = false, want true")
	}

	// v3 index with zero symbols → false.
	zeroSyms := t.TempDir()
	seedIndex(t, zeroSyms, []FileEntry{{Path: "a.go", Size: 10}}, nil)
	if HasSymbolIndex(zeroSyms) {
		t.Error("HasSymbolIndex on v3 index with 0 symbols = true, want false")
	}
}

// TestLookupSymbols_HitsCacheReadsFromDisk exercises the full disk path:
// seed an index, clear the cache, look up a name, verify results.
func TestLookupSymbols_HitsCacheReadsFromDisk(t *testing.T) {
	edrDir := t.TempDir()
	files := []FileEntry{{Path: "a.go", Size: 100}}
	symbols := []SymbolEntry{
		{FileID: 0, Name: "Foo", Kind: KindFunction, StartLine: 1, EndLine: 2, StartByte: 0, EndByte: 10},
		{FileID: 0, Name: "Bar", Kind: KindStruct, StartLine: 5, EndLine: 6, StartByte: 20, EndByte: 30},
		{FileID: 0, Name: "Foo", Kind: KindFunction, StartLine: 10, EndLine: 11, StartByte: 40, EndByte: 50},
	}
	seedIndex(t, edrDir, files, symbols)
	InvalidateSymbolCache()
	t.Cleanup(InvalidateSymbolCache)

	got := LookupSymbols(edrDir, "Foo")
	if len(got) != 2 {
		t.Fatalf("LookupSymbols(Foo) = %d, want 2", len(got))
	}
	for _, s := range got {
		if s.Name != "Foo" {
			t.Errorf("got %q, want Foo", s.Name)
		}
	}

	// Second call should hit the cache — same results. We can't directly
	// assert "didn't touch disk", but hitting the function twice ensures the
	// cache-return branch of loadSymbolIndexCached gets executed.
	got2 := LookupSymbols(edrDir, "Bar")
	if len(got2) != 1 || got2[0].Name != "Bar" {
		t.Errorf("cached LookupSymbols(Bar) = %+v, want one Bar", got2)
	}

	if miss := LookupSymbols(edrDir, "missing"); len(miss) != 0 {
		t.Errorf("LookupSymbols(missing) = %+v, want empty", miss)
	}
}

// TestLookupSymbols_ReturnsNilWhenNoIndex covers the HasSymbolIndex=false
// guard — LookupSymbols must not panic on a brand-new edrDir.
func TestLookupSymbols_ReturnsNilWhenNoIndex(t *testing.T) {
	edrDir := t.TempDir()
	InvalidateSymbolCache()
	t.Cleanup(InvalidateSymbolCache)
	if got := LookupSymbols(edrDir, "Foo"); got != nil {
		t.Errorf("no-index LookupSymbols = %+v, want nil", got)
	}
}

// TestLookupSymbolsByHash_RoundTripsViaDisk verifies the hash-keyed disk
// lookup returns the same entries as the name-keyed one.
func TestLookupSymbolsByHash_RoundTripsViaDisk(t *testing.T) {
	edrDir := t.TempDir()
	files := []FileEntry{{Path: "a.go", Size: 100}}
	symbols := []SymbolEntry{
		{FileID: 0, Name: "Alpha", Kind: KindFunction, EndByte: 10},
		{FileID: 0, Name: "Beta", Kind: KindFunction, EndByte: 20},
	}
	seedIndex(t, edrDir, files, symbols)
	InvalidateSymbolCache()
	t.Cleanup(InvalidateSymbolCache)

	got := LookupSymbolsByHash(edrDir, NameHash("Alpha"))
	if len(got) != 1 || got[0].Name != "Alpha" {
		t.Errorf("LookupSymbolsByHash(Alpha) = %+v, want one Alpha", got)
	}
	if got := LookupSymbolsByHash(edrDir, NameHash("missing")); len(got) != 0 {
		t.Errorf("LookupSymbolsByHash(missing) = %+v, want empty", got)
	}
}

func TestLookupSymbolsByHash_ReturnsNilWhenNoIndex(t *testing.T) {
	edrDir := t.TempDir()
	InvalidateSymbolCache()
	t.Cleanup(InvalidateSymbolCache)
	if got := LookupSymbolsByHash(edrDir, NameHash("Foo")); got != nil {
		t.Errorf("no-index LookupSymbolsByHash = %+v, want nil", got)
	}
}

// TestLookupSymbolsWithIDs_DiskReturnsIDs verifies the ID-returning disk
// lookup pairs each symbol with a valid position into the cached table.
func TestLookupSymbolsWithIDs_DiskReturnsIDs(t *testing.T) {
	edrDir := t.TempDir()
	files := []FileEntry{{Path: "a.go", Size: 100}}
	symbols := []SymbolEntry{
		{FileID: 0, Name: "Target", Kind: KindFunction, StartLine: 1, EndByte: 10},
		{FileID: 0, Name: "Other", Kind: KindFunction, StartLine: 2, EndByte: 20},
		{FileID: 0, Name: "Target", Kind: KindFunction, StartLine: 3, EndByte: 30},
	}
	seedIndex(t, edrDir, files, symbols)
	InvalidateSymbolCache()
	t.Cleanup(InvalidateSymbolCache)

	got := LookupSymbolsWithIDs(edrDir, "Target")
	if len(got) != 2 {
		t.Fatalf("LookupSymbolsWithIDs(Target) = %d, want 2", len(got))
	}
	// IndexID must be valid for the cached symbol table.
	allSyms, _ := LoadAllSymbols(edrDir)
	for _, e := range got {
		if int(e.IndexID) >= len(allSyms) {
			t.Fatalf("IndexID %d out of range (len=%d)", e.IndexID, len(allSyms))
		}
		if allSyms[e.IndexID].Name != "Target" {
			t.Errorf("IndexID %d → %q, want Target", e.IndexID, allSyms[e.IndexID].Name)
		}
	}
}

func TestLookupSymbolsWithIDs_ReturnsNilWhenNoIndex(t *testing.T) {
	edrDir := t.TempDir()
	InvalidateSymbolCache()
	t.Cleanup(InvalidateSymbolCache)
	if got := LookupSymbolsWithIDs(edrDir, "Foo"); got != nil {
		t.Errorf("no-index LookupSymbolsWithIDs = %+v, want nil", got)
	}
}

// TestLoadAllSymbols_ReturnsEntireTable covers the whole-table loader that
// ondemand.go uses to enumerate indexed symbols.
func TestLoadAllSymbols_ReturnsEntireTable(t *testing.T) {
	edrDir := t.TempDir()
	files := []FileEntry{
		{Path: "a.go", Size: 100},
		{Path: "b.go", Size: 200},
	}
	symbols := []SymbolEntry{
		{FileID: 0, Name: "A1", Kind: KindFunction, EndByte: 10},
		{FileID: 1, Name: "B1", Kind: KindFunction, EndByte: 20},
		{FileID: 1, Name: "B2", Kind: KindStruct, EndByte: 30},
	}
	seedIndex(t, edrDir, files, symbols)
	InvalidateSymbolCache()
	t.Cleanup(InvalidateSymbolCache)

	gotSyms, gotFiles := LoadAllSymbols(edrDir)
	if len(gotSyms) != 3 {
		t.Fatalf("LoadAllSymbols returned %d symbols, want 3", len(gotSyms))
	}
	if len(gotFiles) != 2 {
		t.Fatalf("LoadAllSymbols returned %d files, want 2", len(gotFiles))
	}
	names := map[string]bool{}
	for _, s := range gotSyms {
		names[s.Name] = true
	}
	for _, want := range []string{"A1", "B1", "B2"} {
		if !names[want] {
			t.Errorf("symbol %q missing from LoadAllSymbols result", want)
		}
	}
}

func TestLoadAllSymbols_ReturnsNilNilWhenNoIndex(t *testing.T) {
	edrDir := t.TempDir()
	InvalidateSymbolCache()
	t.Cleanup(InvalidateSymbolCache)
	syms, files := LoadAllSymbols(edrDir)
	if syms != nil || files != nil {
		t.Errorf("no-index LoadAllSymbols = (%+v, %+v), want (nil, nil)", syms, files)
	}
}

// TestLoadSymbolIndexCached_GhostFilter documents the non-obvious behavior
// in loadSymbolIndexCached: symbol entries whose EndByte exceeds the file's
// size are silently dropped. This guards against corrupt on-disk indices
// leaking through to callers.
func TestLoadSymbolIndexCached_GhostFilter(t *testing.T) {
	edrDir := t.TempDir()
	files := []FileEntry{{Path: "a.go", Size: 50}}
	symbols := []SymbolEntry{
		{FileID: 0, Name: "Live", Kind: KindFunction, EndByte: 30}, // in-range
		{FileID: 0, Name: "Ghost", Kind: KindFunction, EndByte: 999}, // past EOF
	}
	seedIndex(t, edrDir, files, symbols)
	InvalidateSymbolCache()
	t.Cleanup(InvalidateSymbolCache)

	gotSyms, _ := LoadAllSymbols(edrDir)
	if len(gotSyms) != 1 {
		t.Fatalf("ghost filter produced %d symbols, want 1: %+v", len(gotSyms), gotSyms)
	}
	if gotSyms[0].Name != "Live" {
		t.Errorf("surviving symbol = %q, want Live", gotSyms[0].Name)
	}
}

// TestInvalidateSymbolCache_ForcesReload verifies the cache actually
// honors invalidation — a seed/load/reseed/load sequence must reflect the
// second seed rather than returning stale results.
func TestInvalidateSymbolCache_ForcesReload(t *testing.T) {
	edrDir := t.TempDir()
	seedIndex(t, edrDir,
		[]FileEntry{{Path: "a.go", Size: 100}},
		[]SymbolEntry{{FileID: 0, Name: "First", Kind: KindFunction, EndByte: 10}},
	)
	InvalidateSymbolCache()
	t.Cleanup(InvalidateSymbolCache)

	if got := LookupSymbols(edrDir, "First"); len(got) != 1 {
		t.Fatalf("first seed not visible: got %+v", got)
	}

	// Reseed with a different symbol; without invalidation, the cached view
	// would still see "First".
	seedIndex(t, edrDir,
		[]FileEntry{{Path: "a.go", Size: 100}},
		[]SymbolEntry{{FileID: 0, Name: "Second", Kind: KindFunction, EndByte: 10}},
	)
	if got := LookupSymbols(edrDir, "Second"); len(got) != 0 {
		t.Fatal("without invalidation the re-seed should not be visible")
	}

	InvalidateSymbolCache()
	got := LookupSymbols(edrDir, "Second")
	if len(got) != 1 || got[0].Name != "Second" {
		t.Fatalf("after invalidate, LookupSymbols(Second) = %+v, want one Second", got)
	}
	if stale := LookupSymbols(edrDir, "First"); len(stale) != 0 {
		t.Errorf("after invalidate, old name still visible: %+v", stale)
	}
}
