package session

import (
	"os"
	"path/filepath"
	"testing"
)

// The tests in this file cover the small public API surface that wasn't
// hit by the existing unit tests: stats, run-output cache, repo-root,
// file-mtime tracking, file-hash tracker, block-seen map, and the thin
// LoadSession / WriteSessionMapping / checkpoint API wrappers. They are
// intentionally tight — they pin contracts, not orchestration — so
// regressions in these tiny helpers land as named failures instead of
// bleeding into distant dispatch-level tests.

// --- Stats ---

func TestResetAndGetStats_RoundTrip(t *testing.T) {
	s := New()
	// New session starts at zero.
	d, b := s.GetStats()
	if d != 0 || b != 0 {
		t.Fatalf("initial stats = (%d, %d), want (0, 0)", d, b)
	}
	// Poke the fields directly — there's no public setter; this mirrors
	// how PostProcess accumulates counts.
	s.mu.Lock()
	s.stats.DeltaReads = 3
	s.stats.BodyDedup = 7
	s.mu.Unlock()

	d, b = s.GetStats()
	if d != 3 || b != 7 {
		t.Errorf("GetStats = (%d, %d), want (3, 7)", d, b)
	}

	s.ResetStats()
	d, b = s.GetStats()
	if d != 0 || b != 0 {
		t.Errorf("after ResetStats, GetStats = (%d, %d), want (0, 0)", d, b)
	}
}

// --- Run-output cache ---

func TestRunOutputCache_NewStoreUnchangedClear(t *testing.T) {
	s := New()
	key := "go test ./..."
	out := "ok  pkg1 0.5s\nok  pkg2 1.0s\n"

	if got := s.CheckRunOutput(key, out); got != "new" {
		t.Errorf("first check = %q, want \"new\"", got)
	}

	s.StoreRunOutput(key, out)
	if got := s.CheckRunOutput(key, out); got != "unchanged" {
		t.Errorf("after store, check same output = %q, want \"unchanged\"", got)
	}
	if got := s.CheckRunOutput(key, out+"\n"); got != "new" {
		t.Errorf("after store, check different output = %q, want \"new\"", got)
	}

	s.ClearRunOutput(key)
	if got := s.CheckRunOutput(key, out); got != "new" {
		t.Errorf("after clear, check = %q, want \"new\"", got)
	}
}

func TestCheckRunOutput_NilMapReturnsNew(t *testing.T) {
	// Directly poke RunHashes=nil to exercise the defensive branch.
	s := &Session{}
	if got := s.CheckRunOutput("any", "any"); got != "new" {
		t.Errorf("nil RunHashes map returned %q, want \"new\"", got)
	}
}

// --- Repo root ---

func TestRepoRoot_SetAndGet(t *testing.T) {
	s := New()
	if got := s.RepoRoot(); got != "" {
		t.Errorf("initial RepoRoot = %q, want \"\"", got)
	}
	s.SetRepoRoot("/tmp/myrepo")
	if got := s.RepoRoot(); got != "/tmp/myrepo" {
		t.Errorf("RepoRoot after Set = %q, want /tmp/myrepo", got)
	}
}

// --- File mtime tracking ---

func TestFileMtimes_RecordUpdateClear(t *testing.T) {
	s := New()
	if got := s.GetFileMtimes(); len(got) != 0 {
		t.Errorf("initial GetFileMtimes = %d entries, want 0", len(got))
	}

	s.RecordFileMtime("a.go", 100, "hashA", "op1")
	s.RecordFileMtime("b.go", 200, "hashB", "op2")
	got := s.GetFileMtimes()
	if len(got) != 2 {
		t.Fatalf("after 2 records, GetFileMtimes = %d entries, want 2", len(got))
	}
	if got["a.go"].Mtime != 100 || got["a.go"].Hash != "hashA" || got["a.go"].OpID != "op1" {
		t.Errorf("a.go entry = %+v, want {100, hashA, op1}", got["a.go"])
	}

	// Returned map must be a defensive copy.
	got["a.go"] = FileMtimeEntry{Mtime: -1, Hash: "bad", OpID: "bad"}
	if fresh := s.GetFileMtimes(); fresh["a.go"].Mtime != 100 {
		t.Error("GetFileMtimes did not return a copy — caller mutation leaked into session")
	}

	// UpdateFileMtime touches only the mtime field.
	s.UpdateFileMtime("a.go", 150)
	e := s.GetFileMtimes()["a.go"]
	if e.Mtime != 150 || e.Hash != "hashA" || e.OpID != "op1" {
		t.Errorf("after UpdateFileMtime, entry = %+v, want Mtime=150 with hash/op unchanged", e)
	}

	// UpdateFileMtime on an unknown file must not create an entry.
	s.UpdateFileMtime("nope.go", 999)
	if _, ok := s.GetFileMtimes()["nope.go"]; ok {
		t.Error("UpdateFileMtime created an entry for an unknown file")
	}

	s.ClearFileMtime("a.go")
	if _, ok := s.GetFileMtimes()["a.go"]; ok {
		t.Error("ClearFileMtime did not remove a.go")
	}
	if _, ok := s.GetFileMtimes()["b.go"]; !ok {
		t.Error("ClearFileMtime incorrectly dropped b.go")
	}
}

// --- File hash tracker ---

func TestCheckAndRefreshFileHash(t *testing.T) {
	s := New()
	if got := s.CheckFileHash("a.go"); got != "" {
		t.Errorf("unseen file, CheckFileHash = %q, want \"\"", got)
	}
	s.RefreshFileHash("a.go", "hash1")
	if got := s.CheckFileHash("a.go"); got != "hash1" {
		t.Errorf("after refresh, CheckFileHash = %q, want hash1", got)
	}
	s.RefreshFileHash("a.go", "hash2")
	if got := s.CheckFileHash("a.go"); got != "hash2" {
		t.Errorf("refresh overwrite, CheckFileHash = %q, want hash2", got)
	}
}

func TestCheckFileHash_NilMapSafe(t *testing.T) {
	s := &Session{}
	if got := s.CheckFileHash("any"); got != "" {
		t.Errorf("nil FileHashes map returned %q, want \"\"", got)
	}
}

// --- Block-seen map ---

func TestBlockSeen_MarkAndCheck(t *testing.T) {
	s := New()
	if s.IsBlockSeen("k1") {
		t.Error("unseen key reported as seen")
	}
	s.MarkBlockSeen("k1")
	if !s.IsBlockSeen("k1") {
		t.Error("marked key not reported as seen")
	}
	if s.IsBlockSeen("k2") {
		t.Error("different key reported as seen")
	}
}

// --- LoadSession / WriteSessionMapping ---

func TestLoadSession_EphemeralWhenNoSessionID(t *testing.T) {
	// Clear every env that could produce an id so ResolveSessionID must
	// fall through to PPID — and point that at an isolated tree.
	t.Setenv("EDR_SESSION", "")
	os.Unsetenv("EDR_SESSION")
	t.Setenv("EDR_HOME", t.TempDir())

	// In a non-git dir, resolveByPPID auto-creates an id; but LoadSession's
	// ephemeral branch triggers when ResolveSessionID returns "". To hit
	// that reliably, force it via the public env first.
	t.Setenv("EDR_SESSION", "")
	sess, save := LoadSession(t.TempDir())
	if sess == nil {
		t.Fatal("LoadSession returned nil session")
	}
	// Save should be callable without panicking, even in the ephemeral case.
	save()
}

func TestLoadSession_UsesIDFromEnv(t *testing.T) {
	edrDir := t.TempDir()
	sessDir := filepath.Join(edrDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Seed a session file under the chosen id.
	id := "pinned-session"
	t.Setenv("EDR_SESSION", id)

	first, save := LoadSession(edrDir, "/repo/root")
	if first.RepoRoot() != "/repo/root" {
		t.Errorf("LoadSession did not plumb repoRoot: %q", first.RepoRoot())
	}
	first.RecordOp("edit", "a.go", "Foo", "replace_text", "text_replaced", true)
	save()

	// Second load must pick up the persisted op log.
	second, _ := LoadSession(edrDir)
	if len(second.GetRecentOps(0)) != 1 {
		t.Errorf("second LoadSession lost op log: %+v", second.GetRecentOps(0))
	}

	// And the session file must exist at the expected path.
	if _, err := os.Stat(filepath.Join(sessDir, id+".json")); err != nil {
		t.Errorf("session file not persisted at id.json: %v", err)
	}
}

func TestWriteSessionMapping_CreatesPPIDFile(t *testing.T) {
	sessDir := t.TempDir()
	id := "abc123"
	WriteSessionMapping(sessDir, id)

	// Find the ppid_* file it created — we don't predict the PID, just
	// require that exactly one mapping landed and it contains the ID.
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		t.Fatal(err)
	}
	var mappings []string
	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) > 5 && e.Name()[:5] == "ppid_" {
			mappings = append(mappings, e.Name())
		}
	}
	if len(mappings) != 1 {
		t.Fatalf("WriteSessionMapping produced %d ppid_ files, want 1: %v", len(mappings), mappings)
	}
	data, err := os.ReadFile(filepath.Join(sessDir, mappings[0]))
	if err != nil {
		t.Fatal(err)
	}
	// Content is "id\nstartTime" or just "id".
	got := string(data)
	if got != id && got[:len(id)] != id {
		t.Errorf("mapping content = %q, want prefix %q", got, id)
	}
}

// --- Checkpoint API wrappers ---

func TestLoadCheckpoint_RoundTrips(t *testing.T) {
	sessDir := t.TempDir()
	repoRoot := t.TempDir()

	s := New()
	cp, err := s.CreateCheckpoint(sessDir, repoRoot, "label-x", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadCheckpoint(sessDir, cp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != cp.ID || got.Label != "label-x" {
		t.Errorf("LoadCheckpoint = %+v, want id=%q label=label-x", got, cp.ID)
	}
	if _, err := LoadCheckpoint(sessDir, "cp_missing"); err == nil {
		t.Error("LoadCheckpoint on missing ID = nil err, want not-found error")
	}
}

func TestLatestAutoCheckpoint_PicksMostRecentAuto(t *testing.T) {
	sessDir := t.TempDir()
	repoRoot := t.TempDir()

	// No checkpoints at all — empty string.
	if got := LatestAutoCheckpoint(sessDir); got != "" {
		t.Errorf("empty dir LatestAutoCheckpoint = %q, want \"\"", got)
	}

	s := New()
	// Explicit checkpoints don't count.
	if _, err := s.CreateCheckpoint(sessDir, repoRoot, "", nil); err != nil {
		t.Fatal(err)
	}
	if got := LatestAutoCheckpoint(sessDir); got != "" {
		t.Errorf("with only explicit cps, LatestAutoCheckpoint = %q, want \"\"", got)
	}

	// Add two autos and make sure the second is reported.
	a1, err := s.CreateAutoCheckpoint(sessDir, repoRoot, "first", nil)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := s.CreateAutoCheckpoint(sessDir, repoRoot, "second", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := LatestAutoCheckpoint(sessDir); got != a2.ID {
		t.Errorf("LatestAutoCheckpoint = %q, want %q (expected a2, had a1=%s)", got, a2.ID, a1.ID)
	}
}

func TestCreateAutoCheckpoint_EnforcesCap(t *testing.T) {
	sessDir := t.TempDir()
	repoRoot := t.TempDir()
	s := New()

	// Create one more than the cap — the oldest should get evicted.
	for i := 0; i < MaxUndoStack+1; i++ {
		if _, err := s.CreateAutoCheckpoint(sessDir, repoRoot, "", nil); err != nil {
			t.Fatal(err)
		}
	}

	autos := 0
	for _, cp := range ListCheckpoints(sessDir) {
		if len(cp.ID) > len("cp_auto_") && cp.ID[:len("cp_auto_")] == "cp_auto_" {
			autos++
		}
	}
	if autos != MaxUndoStack {
		t.Errorf("after %d creates, autos = %d, want %d", MaxUndoStack+1, autos, MaxUndoStack)
	}
}

func TestAppendFilesToCheckpoint_AddsAndDedups(t *testing.T) {
	sessDir := t.TempDir()
	repoRoot := t.TempDir()

	// Seed a file that exists and one that doesn't.
	if err := os.WriteFile(filepath.Join(repoRoot, "exists.go"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := New()
	cp, err := s.CreateCheckpoint(sessDir, repoRoot, "base", nil)
	if err != nil {
		t.Fatal(err)
	}

	// No-ops: empty cp ID, empty paths.
	if err := s.AppendFilesToCheckpoint(sessDir, repoRoot, "", []string{"a"}); err != nil {
		t.Errorf("empty cpID should be a no-op, got %v", err)
	}
	if err := s.AppendFilesToCheckpoint(sessDir, repoRoot, cp.ID, nil); err != nil {
		t.Errorf("empty paths should be a no-op, got %v", err)
	}

	if err := s.AppendFilesToCheckpoint(sessDir, repoRoot, cp.ID, []string{"exists.go", "missing.go", ""}); err != nil {
		t.Fatal(err)
	}

	// Second append with the same path should be a no-op (first-write wins).
	if err := os.WriteFile(filepath.Join(repoRoot, "exists.go"), []byte("BETA"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendFilesToCheckpoint(sessDir, repoRoot, cp.ID, []string{"exists.go"}); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadCheckpoint(sessDir, cp.ID)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string][]byte{}
	for _, f := range reloaded.Files {
		byPath[f.Path] = f.Content
	}
	if got, ok := byPath["exists.go"]; !ok || string(got) != "alpha" {
		t.Errorf("exists.go snapshot = %q (ok=%v), want alpha (first write)", string(got), ok)
	}
	missing, ok := byPath["missing.go"]
	if !ok {
		t.Error("missing.go not recorded")
	}
	if ok && missing != nil {
		t.Errorf("missing.go should have nil content (deletion marker), got %q", string(missing))
	}
}

func TestAppendFilesToCheckpoint_MissingCheckpointReturnsError(t *testing.T) {
	sessDir := t.TempDir()
	s := New()
	err := s.AppendFilesToCheckpoint(sessDir, t.TempDir(), "cp_nope", []string{"a.go"})
	if err == nil {
		t.Error("AppendFilesToCheckpoint on unknown cp_id returned nil, want error")
	}
}

func TestPatchCheckpointFiles_AddsOnlyNew(t *testing.T) {
	sessDir := t.TempDir()
	repoRoot := t.TempDir()

	// Create a checkpoint that already snapshots a.go.
	if err := os.WriteFile(filepath.Join(repoRoot, "a.go"), []byte("preA"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New()
	cp, err := s.CreateCheckpoint(sessDir, repoRoot, "", []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}

	// Patch with one existing path (should be skipped) and one new path.
	err = PatchCheckpointFiles(sessDir, cp.ID, repoRoot, map[string][]byte{
		"a.go": []byte("NOT-USED-because-already-present"),
		"b.go": []byte("preB"),
	})
	if err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadCheckpoint(sessDir, cp.ID)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]string{}
	for _, f := range reloaded.Files {
		byPath[f.Path] = string(f.Content)
	}
	if byPath["a.go"] != "preA" {
		t.Errorf("a.go content = %q, want preA (must not be overwritten)", byPath["a.go"])
	}
	if byPath["b.go"] != "preB" {
		t.Errorf("b.go content = %q, want preB", byPath["b.go"])
	}

	// No-op when all paths already present — should not error and should
	// not rewrite.
	if err := PatchCheckpointFiles(sessDir, cp.ID, repoRoot, map[string][]byte{"a.go": []byte("x")}); err != nil {
		t.Errorf("no-op PatchCheckpointFiles errored: %v", err)
	}
}

func TestPatchCheckpointFiles_UnknownCheckpointReturnsError(t *testing.T) {
	sessDir := t.TempDir()
	err := PatchCheckpointFiles(sessDir, "cp_missing", t.TempDir(), map[string][]byte{"x": nil})
	if err == nil {
		t.Error("PatchCheckpointFiles on unknown id returned nil, want error")
	}
}
