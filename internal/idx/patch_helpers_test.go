package idx

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Targeted unit tests for the helpers that PatchDirtyFiles was
// decomposed into. Integration behavior is covered by the phantom +
// patch-extract tests; these cover the helpers' contracts directly so
// regressions land with small, readable failures.

func TestRebuildFileTable_DropsDeleted(t *testing.T) {
	old := []FileEntry{
		{Path: "a.go", Mtime: 1, Size: 10},
		{Path: "b.go", Mtime: 2, Size: 20},
		{Path: "c.go", Mtime: 3, Size: 30},
	}
	deleted := map[string]bool{"b.go": true}
	files, oldIDToNewID, newByPath := rebuildFileTable(old, deleted, nil)

	wantFiles := []FileEntry{
		{Path: "a.go", Mtime: 1, Size: 10},
		{Path: "c.go", Mtime: 3, Size: 30},
	}
	if !reflect.DeepEqual(files, wantFiles) {
		t.Fatalf("files=%+v, want %+v", files, wantFiles)
	}
	// b.go was at id 1 and should have no mapping.
	if _, ok := oldIDToNewID[1]; ok {
		t.Fatalf("deleted file id 1 should not be in oldIDToNewID: %+v", oldIDToNewID)
	}
	if oldIDToNewID[0] != 0 || oldIDToNewID[2] != 1 {
		t.Fatalf("id remap wrong: %+v", oldIDToNewID)
	}
	if newByPath["a.go"] != 0 || newByPath["c.go"] != 1 {
		t.Fatalf("newByPath wrong: %+v", newByPath)
	}
}

func TestRebuildFileTable_ReplacesModified(t *testing.T) {
	old := []FileEntry{
		{Path: "a.go", Mtime: 1, Size: 10},
		{Path: "b.go", Mtime: 2, Size: 20},
	}
	patches := []patchEntry{
		{entry: FileEntry{Path: "b.go", Mtime: 99, Size: 99}},
	}
	files, _, newByPath := rebuildFileTable(old, nil, patches)

	if len(files) != 2 {
		t.Fatalf("want 2 files, got %d: %+v", len(files), files)
	}
	if files[1].Mtime != 99 || files[1].Size != 99 {
		t.Fatalf("b.go not replaced in place: %+v", files[1])
	}
	// a.go must be untouched.
	if files[0].Mtime != 1 || files[0].Size != 10 {
		t.Fatalf("a.go disturbed: %+v", files[0])
	}
	if newByPath["b.go"] != 1 {
		t.Fatalf("newByPath[b.go]=%d want 1", newByPath["b.go"])
	}
}

func TestRebuildFileTable_AppendsNew(t *testing.T) {
	old := []FileEntry{
		{Path: "a.go", Mtime: 1, Size: 10},
	}
	patches := []patchEntry{
		{entry: FileEntry{Path: "new.go", Mtime: 7, Size: 7}},
	}
	files, _, newByPath := rebuildFileTable(old, nil, patches)
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %d", len(files))
	}
	if files[1].Path != "new.go" {
		t.Fatalf("new file not appended: %+v", files)
	}
	if newByPath["new.go"] != 1 {
		t.Fatalf("newByPath[new.go]=%d want 1", newByPath["new.go"])
	}
}

func TestRebuildTrigramMap_FiltersDeletedAndDirty(t *testing.T) {
	// Old index has three files: a, b (deleted), c. Trigram "abc"
	// is in a+b+c; trigram "xyz" only in b. After patching with b
	// deleted and a modified (dirty) with new trigram "new":
	//   - "abc" should remap so only c survives.
	//   - "xyz" should be dropped entirely.
	//   - "new" should point at a's new ID.
	triA := Trigram{'a', 'b', 'c'}
	triX := Trigram{'x', 'y', 'z'}
	triN := Trigram{'n', 'e', 'w'}

	oldFiles := []FileEntry{
		{Path: "a.go"}, // id 0 — modified (dirty)
		{Path: "b.go"}, // id 1 — deleted
		{Path: "c.go"}, // id 2 — unchanged
	}
	raw := make(map[Trigram][]uint32)
	raw[triA] = []uint32{0, 1, 2}
	raw[triX] = []uint32{1}
	postings, entries := BuildPostings(raw)
	old := &Snapshot{
		Files:    oldFiles,
		Trigrams: entries,
		Postings: postings,
	}

	dirtySet := map[string]bool{"a.go": true, "b.go": true}
	// rebuildFileTable drops b.go (deleted) and keeps a+c, then
	// replaces a with the patched entry.
	patches := []patchEntry{
		{entry: FileEntry{Path: "a.go"}, tris: []Trigram{triN}},
	}
	deletedSet := map[string]bool{"b.go": true}
	files, oldIDToNewID, newByPath := rebuildFileTable(oldFiles, deletedSet, patches)

	got := rebuildTrigramMap(old, dirtySet, oldIDToNewID, patches, newByPath)

	aNew := newByPath["a.go"]
	cNew := newByPath["c.go"]

	// "abc" keeps only c (a was dirty, b was deleted).
	if !reflect.DeepEqual(got[triA], []uint32{cNew}) {
		t.Fatalf("triA postings=%v, want [%d]", got[triA], cNew)
	}
	// "xyz" fully dropped.
	if ids, ok := got[triX]; ok {
		t.Fatalf("triX should be dropped, got %v", ids)
	}
	// "new" comes from the fresh patch with a's new ID.
	if !reflect.DeepEqual(got[triN], []uint32{aNew}) {
		t.Fatalf("triN postings=%v, want [%d]", got[triN], aNew)
	}
	// files table shape sanity
	if len(files) != 2 {
		t.Fatalf("len(files)=%d, want 2", len(files))
	}
}

// TestRebuildFileTable_IDMappingsAreSelfConsistent verifies the ID-integrity
// invariant that makes every downstream caller correct: for every entry in
// oldIDToNewID and newByPath, the resolved new ID must point at a files
// slot whose path matches. Silent drift here would yield wrong file paths
// for trigram/symbol postings.
func TestRebuildFileTable_IDMappingsAreSelfConsistent(t *testing.T) {
	old := []FileEntry{
		{Path: "a.go"},
		{Path: "b.go"},
		{Path: "c.go"},
		{Path: "d.go"},
	}
	deleted := map[string]bool{"b.go": true}
	patches := []patchEntry{
		{entry: FileEntry{Path: "new.go"}}, // append
		{entry: FileEntry{Path: "c.go"}},   // replace in place
	}
	files, oldIDToNewID, newByPath := rebuildFileTable(old, deleted, patches)

	for oldID, newID := range oldIDToNewID {
		if int(newID) >= len(files) {
			t.Fatalf("oldID %d → newID %d out of range (len=%d)", oldID, newID, len(files))
		}
		if files[newID].Path != old[oldID].Path {
			t.Errorf("oldID %d (%s) → newID %d (%s): path mismatch",
				oldID, old[oldID].Path, newID, files[newID].Path)
		}
	}
	for path, newID := range newByPath {
		if int(newID) >= len(files) {
			t.Fatalf("newByPath[%s]=%d out of range (len=%d)", path, newID, len(files))
		}
		if files[newID].Path != path {
			t.Errorf("newByPath[%s]=%d but files[%d].Path=%s",
				path, newID, newID, files[newID].Path)
		}
	}
	if _, ok := newByPath["b.go"]; ok {
		t.Error("deleted b.go should not appear in newByPath")
	}
}

// TestRebuildTrigramMap_UnchangedFilesGetRemappedIDs covers the
// remap-but-keep case: a trigram shared across a surviving-unchanged
// file and a deleted file must keep only the survivor, but its ID
// must be the *new* ID, not the old one. Missing this would leave
// postings pointing at stale file-table slots after a delete.
func TestRebuildTrigramMap_UnchangedFilesGetRemappedIDs(t *testing.T) {
	triFoo := Trigram{'f', 'o', 'o'}
	raw := map[Trigram][]uint32{triFoo: {0, 2}}
	postings, entries := BuildPostings(raw)
	old := &Snapshot{
		Files: []FileEntry{
			{Path: "a.go"},
			{Path: "b.go"},
			{Path: "c.go"},
		},
		Trigrams: entries,
		Postings: postings,
	}
	deletedSet := map[string]bool{"b.go": true}
	_, oldIDToNewID, newByPath := rebuildFileTable(old.Files, deletedSet, nil)
	// Nothing is "dirty" — b.go was deleted, not modified. a.go and c.go
	// survive unchanged and their postings must remap.
	got := rebuildTrigramMap(old, deletedSet, oldIDToNewID, nil, newByPath)
	// After the delete: a.go is still ID 0, c.go is now ID 1.
	want := []uint32{0, 1}
	if !reflect.DeepEqual(got[triFoo], want) {
		t.Fatalf("triFoo postings=%v, want %v (IDs must remap from {0,2} to {0,1})", got[triFoo], want)
	}
}

// collectPatches covers the edge cases of the first pass in PatchDirtyFiles:
// stat failure → deletedSet, read/binary → silent skip, success → full
// patchEntry with trigrams and (optionally) symbols.

func TestCollectPatches_StatFailureMarksDeleted(t *testing.T) {
	root := t.TempDir()
	patches, deletedSet := collectPatches(root, []string{"missing.go"}, nil)
	if len(patches) != 0 {
		t.Errorf("want 0 patches for missing file, got %d: %+v", len(patches), patches)
	}
	if !deletedSet["missing.go"] {
		t.Errorf("missing.go should be in deletedSet: %+v", deletedSet)
	}
}

func TestCollectPatches_ReadableFileProducesTrigramsAndNoSymsWithoutExtractor(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.go")
	if err := os.WriteFile(p, []byte("package a\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patches, deletedSet := collectPatches(root, []string{"a.go"}, nil)
	if len(patches) != 1 {
		t.Fatalf("want 1 patch, got %d", len(patches))
	}
	if patches[0].entry.Path != "a.go" {
		t.Errorf("wrong path: %+v", patches[0].entry)
	}
	if len(patches[0].tris) == 0 {
		t.Error("expected trigrams")
	}
	if patches[0].syms != nil {
		t.Errorf("nil extractor should produce nil syms, got %+v", patches[0].syms)
	}
	if len(deletedSet) != 0 {
		t.Errorf("deletedSet should be empty, got %+v", deletedSet)
	}
}

func TestCollectPatches_ExtractorProducesSymbols(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.go")
	if err := os.WriteFile(p, []byte("package a\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	extract := func(path string, data []byte) []SymbolEntry {
		return []SymbolEntry{{Name: "Foo", Kind: KindFunction}}
	}
	patches, _ := collectPatches(root, []string{"a.go"}, extract)
	if len(patches) != 1 {
		t.Fatalf("want 1 patch, got %d", len(patches))
	}
	if len(patches[0].syms) != 1 || patches[0].syms[0].Name != "Foo" {
		t.Errorf("extractor output lost: %+v", patches[0].syms)
	}
}

func TestCollectPatches_BinaryFileIsSilentlySkipped(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "blob.bin")
	// NUL byte triggers isBinary.
	if err := os.WriteFile(p, []byte{0x00, 0x01, 0x02, 0x03}, 0o644); err != nil {
		t.Fatal(err)
	}
	patches, deletedSet := collectPatches(root, []string{"blob.bin"}, nil)
	// Neither in patches (would re-index) nor in deletedSet (would evict) —
	// binary files preserve the old entry on the theory that a transient
	// IO or detection hiccup shouldn't destroy a known-good record.
	if len(patches) != 0 {
		t.Errorf("binary file should not produce patch, got %+v", patches)
	}
	if deletedSet["blob.bin"] {
		t.Errorf("binary file should not be marked deleted: %+v", deletedSet)
	}
}

// rebuildSymbolTable exercise via a real on-disk index. seedIndex writes
// a minimal Snapshot that loadIndex() can read back — the helper lets
// tests construct arbitrary (files, symbols) pairs without running the
// full Build path.
func seedIndex(t *testing.T, edrDir string, files []FileEntry, symbols []SymbolEntry) {
	t.Helper()
	if err := os.MkdirAll(edrDir, 0o700); err != nil {
		t.Fatal(err)
	}
	namePostings, namePosts := BuildNamePostings(symbols)
	d := &Snapshot{
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
	if err := os.WriteFile(filepath.Join(edrDir, MainFile), d.Marshal(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRebuildSymbolTable_ReturnsNilWhenOldHasNoSymbols(t *testing.T) {
	edrDir := t.TempDir()
	old := &Snapshot{Header: Header{NumSymbols: 0}}
	syms, namePosts, namePostings := rebuildSymbolTable(edrDir, old, nil, nil, nil, nil)
	if syms != nil || namePosts != nil || namePostings != nil {
		t.Errorf("want all-nil for zero-symbol old index, got syms=%v posts=%v postings=%v",
			syms, namePosts, namePostings)
	}
}

// Delete the middle file; surviving symbols must remap to post-renumber IDs.
func TestRebuildSymbolTable_RemapsAfterDeletion(t *testing.T) {
	edrDir := t.TempDir()
	files := []FileEntry{
		{Path: "a.go"},
		{Path: "b.go"},
		{Path: "c.go"},
	}
	symbols := []SymbolEntry{
		{Name: "A", FileID: 0},
		{Name: "B", FileID: 1},
		{Name: "C", FileID: 2},
	}
	seedIndex(t, edrDir, files, symbols)

	deletedSet := map[string]bool{"b.go": true}
	newFiles, _, newByPath := rebuildFileTable(files, deletedSet, nil)

	old := &Snapshot{Header: Header{NumSymbols: 3}, Files: files}
	syms, _, _ := rebuildSymbolTable(edrDir, old, newFiles, deletedSet, nil, newByPath)

	if len(syms) != 2 {
		t.Fatalf("want 2 surviving symbols, got %d: %+v", len(syms), syms)
	}
	byName := map[string]uint32{}
	for _, s := range syms {
		byName[s.Name] = s.FileID
	}
	if byName["A"] != 0 {
		t.Errorf("A.FileID=%d want 0", byName["A"])
	}
	if byName["C"] != 1 {
		t.Errorf("C.FileID=%d want 1 (remapped from 2)", byName["C"])
	}
	if _, ok := byName["B"]; ok {
		t.Errorf("B should have been dropped (its file was deleted)")
	}
}

func TestRebuildSymbolTable_NilExtractorDropsDirtyWithoutReplacement(t *testing.T) {
	edrDir := t.TempDir()
	files := []FileEntry{{Path: "a.go"}}
	symbols := []SymbolEntry{{Name: "Foo", FileID: 0}}
	seedIndex(t, edrDir, files, symbols)

	dirtySet := map[string]bool{"a.go": true}
	patches := []patchEntry{{entry: FileEntry{Path: "a.go"}, syms: nil}}
	newFiles, _, newByPath := rebuildFileTable(files, nil, patches)

	old := &Snapshot{Header: Header{NumSymbols: 1}, Files: files}
	syms, _, _ := rebuildSymbolTable(edrDir, old, newFiles, dirtySet, patches, newByPath)

	if len(syms) != 0 {
		t.Errorf("nil-extractor path should produce 0 symbols after dirtying the only file, got %+v", syms)
	}
}

func TestRebuildSymbolTable_ExtractorMergesFreshWithRemapped(t *testing.T) {
	edrDir := t.TempDir()
	files := []FileEntry{
		{Path: "a.go"}, // will be modified
		{Path: "b.go"}, // unchanged
	}
	symbols := []SymbolEntry{
		{Name: "OldFoo", FileID: 0},
		{Name: "Bar", FileID: 1},
	}
	seedIndex(t, edrDir, files, symbols)

	dirtySet := map[string]bool{"a.go": true}
	patches := []patchEntry{
		{
			entry: FileEntry{Path: "a.go"},
			syms:  []SymbolEntry{{Name: "NewFoo"}}, // fresh extraction
		},
	}
	newFiles, _, newByPath := rebuildFileTable(files, nil, patches)

	old := &Snapshot{Header: Header{NumSymbols: 2}, Files: files}
	syms, _, _ := rebuildSymbolTable(edrDir, old, newFiles, dirtySet, patches, newByPath)

	byName := map[string]uint32{}
	for _, s := range syms {
		byName[s.Name] = s.FileID
	}
	if _, ok := byName["OldFoo"]; ok {
		t.Errorf("OldFoo should have been dropped (file was dirty)")
	}
	if byName["NewFoo"] != 0 {
		t.Errorf("NewFoo.FileID=%d want 0 (from extractor, re-attached to a.go)", byName["NewFoo"])
	}
	if byName["Bar"] != 1 {
		t.Errorf("Bar.FileID=%d want 1 (unchanged, remapped)", byName["Bar"])
	}
}
