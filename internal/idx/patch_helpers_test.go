package idx

import (
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
	old := &IndexData{
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
