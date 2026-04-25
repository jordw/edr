package idx

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression tests for PatchDirtyFiles's on-disk-file invalidation
// contract. After any patch:
//
//   - popularity.bin must go: scores are computed against the full
//     index and become stale after remap.
//   - refs.bin must go: ForwardOffsets and InvSymIDs index into the
//     symbol table by ID. PatchDirtyFiles renumbers symbol IDs via
//     rebuildSymbolTable, so the old refgraph's IDs point at
//     different (or out-of-range) symbols afterward.
//   - import_graph.bin must go: its path list is frozen at build
//     time. Deleted files still appear as importers; new files are
//     absent.
//
// None of the three is remapped in place. Invalidation trades
// freshness for correctness — the next `edr index` rebuilds them.

// seedMinimalIndex writes a near-empty but loadable trigram.idx so
// loadIndexTrigrams returns non-nil and PatchDirtyFiles reaches its
// invalidation block.
func seedMinimalIndex(t *testing.T, edrDir string) {
	t.Helper()
	if err := os.MkdirAll(edrDir, 0o700); err != nil {
		t.Fatal(err)
	}
	d := &Snapshot{
		Header: Header{NumFiles: 0, NumTrigrams: 0},
	}
	if err := os.WriteFile(filepath.Join(edrDir, MainFile), d.Marshal(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPatchInvalidates_RefGraph(t *testing.T) {
	root := t.TempDir()
	edrDir := filepath.Join(root, ".edr")
	seedMinimalIndex(t, edrDir)

	rg := BuildRefGraphV2(2, [][]string{{"foo"}, {"bar"}})
	if err := WriteRefGraph(edrDir, rg); err != nil {
		t.Fatalf("WriteRefGraph: %v", err)
	}
	if !HasRefGraph(edrDir) {
		t.Fatal("pre-condition: refs.bin not written")
	}

	PatchDirtyFiles(root, edrDir, nil, nil)

	if HasRefGraph(edrDir) {
		t.Error("refs.bin must be removed after PatchDirtyFiles; " +
			"symbol IDs renumber and the graph's InvSymIDs now point at wrong symbols")
	}
}

func TestPatchInvalidates_ImportGraph(t *testing.T) {
	root := t.TempDir()
	edrDir := filepath.Join(root, ".edr")
	seedMinimalIndex(t, edrDir)

	ig := BuildImportGraph([]string{"a.go", "b.go"}, [][2]string{{"a.go", "b.go"}})
	if err := WriteImportGraph(edrDir, ig); err != nil {
		t.Fatalf("WriteImportGraph: %v", err)
	}
	if !HasImportGraph(edrDir) {
		t.Fatal("pre-condition: import_graph.bin not written")
	}

	PatchDirtyFiles(root, edrDir, nil, nil)

	if HasImportGraph(edrDir) {
		t.Error("import_graph.bin must be removed after PatchDirtyFiles; " +
			"its path list is frozen — deleted files stay as phantoms, new files are missing")
	}
}

// Popularity invalidation was already implemented before these changes
// but never had a dedicated regression test. Pin the behavior so it
// doesn't silently regress alongside the other two.
func TestPatchInvalidates_Popularity(t *testing.T) {
	root := t.TempDir()
	edrDir := filepath.Join(root, ".edr")
	seedMinimalIndex(t, edrDir)

	popPath := filepath.Join(edrDir, PopularityFile)
	if err := os.WriteFile(popPath, []byte("dummy popularity data"), 0o600); err != nil {
		t.Fatal(err)
	}

	PatchDirtyFiles(root, edrDir, nil, nil)

	if _, err := os.Stat(popPath); !os.IsNotExist(err) {
		t.Errorf("popularity.bin must be removed after PatchDirtyFiles; stat err=%v", err)
	}
}
