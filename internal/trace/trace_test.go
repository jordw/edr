package trace

import (
	"os"
	"testing"
	"time"
)

func TestCollectorNil(t *testing.T) {
	// All operations on nil Collector/CallBuilder should be no-ops
	var tc *Collector
	cb := tc.BeginCall()
	cb.SetRequest(1, 2, 0, 0, 0, false, false, nil)
	cb.AddEditEvent("f.go", 5, "abc", "def", true)
	cb.AddVerifyEvent("go build", true, 100, 50)
	cb.AddQueryEvent("search", true, 200)
	cb.SetSessionStats(1, 2)
	cb.Finish(100, false, 0)
	tc.Close() // should not panic
}

func TestCollectorOpenCloseEmpty(t *testing.T) {
	dir := t.TempDir()
	tc := NewCollector(dir, "test-1.0")
	if tc == nil {
		t.Fatal("expected non-nil collector")
	}
	if tc.SessionID() == "" {
		t.Fatal("expected non-empty session ID")
	}
	tc.Close()

	// Verify session row exists with ended_at
	db, err := OpenTraceDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var endedAt string
	err = db.QueryRow("SELECT COALESCE(ended_at, '') FROM sessions WHERE id = ?", tc.SessionID()).Scan(&endedAt)
	if err != nil {
		t.Fatal(err)
	}
	if endedAt == "" {
		t.Error("expected ended_at to be set")
	}
}

func TestCollectorRecordAndBench(t *testing.T) {
	dir := t.TempDir()
	tc := NewCollector(dir, "test-1.0+abc123")
	if tc == nil {
		t.Fatal("expected non-nil collector")
	}

	// Record a call with reads and queries
	cb := tc.BeginCall()
	budget := 500
	cb.SetRequest(2, 1, 0, 0, 0, false, false, &budget)
	cb.AddQueryEvent("search", true, 300)
	cb.SetSessionStats(1, 0)
	cb.Finish(800, false, 0)

	// Record a call with edits and verify
	cb2 := tc.BeginCall()
	cb2.SetRequest(0, 0, 2, 0, 0, true, false, nil)
	cb2.AddEditEvent("main.go", 5, "hash1", "hash2", true)
	cb2.AddEditEvent("util.go", 3, "hash3", "hash4", true)
	cb2.AddVerifyEvent("go build ./...", true, 250, 100)
	cb2.SetSessionStats(0, 0)
	cb2.Finish(400, false, 1)

	// Wait for flush
	tc.Close()

	// Bench the session
	db, err := OpenTraceDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	result, err := BenchSession(db, tc.SessionID())
	if err != nil {
		t.Fatal(err)
	}

	if result.TotalCalls != 2 {
		t.Errorf("expected 2 calls, got %d", result.TotalCalls)
	}
	if result.TotalReads != 2 {
		t.Errorf("expected 2 reads, got %d", result.TotalReads)
	}
	if result.TotalQueries != 1 {
		t.Errorf("expected 1 query, got %d", result.TotalQueries)
	}
	if result.TotalEdits != 2 {
		t.Errorf("expected 2 edits, got %d", result.TotalEdits)
	}
	if result.TotalVerifies != 1 {
		t.Errorf("expected 1 verify, got %d", result.TotalVerifies)
	}
	if result.DeltaReads != 1 {
		t.Errorf("expected 1 delta read, got %d", result.DeltaReads)
	}
	if result.SlimEdits != 0 {
		t.Errorf("expected 0 slim edits, got %d", result.SlimEdits)
	}
	if result.EditFiles != 2 {
		t.Errorf("expected 2 edit files, got %d", result.EditFiles)
	}
	if result.EditOK != 2 {
		t.Errorf("expected 2 ok edits, got %d", result.EditOK)
	}
	if result.VerifyOK != 1 {
		t.Errorf("expected 1 ok verify, got %d", result.VerifyOK)
	}
	if result.Warnings != 1 {
		t.Errorf("expected 1 warning, got %d", result.Warnings)
	}
	if result.TotalTokensEst != 300 { // (800+400)/4
		t.Errorf("expected 300 token est, got %d", result.TotalTokensEst)
	}
	if result.EDRVersion != "test-1.0+abc123" {
		t.Errorf("expected version test-1.0+abc123, got %s", result.EDRVersion)
	}

	// Derived scores
	if result.ReadEfficiency == nil || *result.ReadEfficiency != 0.5 {
		t.Errorf("expected read efficiency 0.5, got %v", result.ReadEfficiency)
	}
	if result.EditSuccessRate == nil || *result.EditSuccessRate != 1.0 {
		t.Errorf("expected edit success rate 1.0, got %v", result.EditSuccessRate)
	}
	if result.VerifyPassRate == nil || *result.VerifyPassRate != 1.0 {
		t.Errorf("expected verify pass rate 1.0, got %v", result.VerifyPassRate)
	}
	if result.TokensPerCall == nil || *result.TokensPerCall != 150.0 {
		t.Errorf("expected 150 tokens/call, got %v", result.TokensPerCall)
	}
	// optimization rate: (1 delta + 0 dedup + 1 slim) / 3 calls = 2/3
	if result.OptimizationRate == nil {
		t.Error("expected optimization rate to be set")
	}
}

func TestBenchSessionMostRecent(t *testing.T) {
	dir := t.TempDir()

	// Create two sessions with enough separation for RFC3339 ordering
	tc1 := NewCollector(dir, "v1")
	tc1.Close()
	time.Sleep(10 * time.Millisecond)
	tc2 := NewCollector(dir, "v2")
	tc2.Close()

	db, err := OpenTraceDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Empty sessionID should pick most recent
	result, err := BenchSession(db, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != tc2.SessionID() {
		t.Errorf("expected session %s, got %s", tc2.SessionID(), result.SessionID)
	}
}

func TestBenchSessionNotFound(t *testing.T) {
	dir := t.TempDir()
	tc := NewCollector(dir, "v1")
	tc.Close()

	db, err := OpenTraceDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = BenchSession(db, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent session")
	}
}

func TestCollectorDoubleClose(t *testing.T) {
	dir := t.TempDir()
	tc := NewCollector(dir, "v1")
	tc.Close()
	tc.Close() // should not panic
}

func TestCollectorBadDir(t *testing.T) {
	tc := NewCollector("/nonexistent/path/that/doesnt/exist", "v1")
	// Should be nil (non-fatal)
	if tc != nil {
		tc.Close()
	}
}

func TestOpenTraceDBNotExist(t *testing.T) {
	dir := t.TempDir()
	// Opening a non-existent DB should create it (sqlite creates on open)
	db, err := OpenTraceDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Verify file was created
	if _, err := os.Stat(dir + "/traces.db"); err != nil {
		t.Error("expected traces.db to exist")
	}
}

// --- Issue 4: EDR_SESSION identity ---

func TestCollectorUsesEDRSession(t *testing.T) {
	// When EDR_SESSION is set, the collector should use it as session ID.
	dir := t.TempDir()

	t.Setenv("EDR_SESSION", "test-session-42")
	tc := NewCollector(dir, "test-1.0")
	if tc == nil {
		t.Fatal("expected non-nil collector")
	}
	defer tc.Close()

	if tc.SessionID() != "test-session-42" {
		t.Errorf("SessionID() = %q, want %q", tc.SessionID(), "test-session-42")
	}
}

func TestCollectorFallsBackToTimestamp(t *testing.T) {
	// Without EDR_SESSION, the collector should generate a timestamp-based ID.
	dir := t.TempDir()

	t.Setenv("EDR_SESSION", "")
	tc := NewCollector(dir, "test-1.0")
	if tc == nil {
		t.Fatal("expected non-nil collector")
	}
	defer tc.Close()

	if tc.SessionID() == "" {
		t.Error("expected non-empty session ID fallback")
	}
	if tc.SessionID() == "test-session-42" {
		t.Error("expected timestamp ID, not stale env var")
	}
}

func TestBenchSessionRespectsEDRSession(t *testing.T) {
	// bench-session with EDR_SESSION should score that session, not most recent.
	dir := t.TempDir()

	// Create two sessions: an older one with EDR_SESSION, and a newer one without.
	t.Setenv("EDR_SESSION", "target-session")
	tc1 := NewCollector(dir, "test-1.0")
	if tc1 == nil {
		t.Fatal("expected non-nil collector")
	}
	cb := tc1.BeginCall()
	cb.SetRequest(3, 0, 0, 0, 0, false, false, nil)
	cb.Finish(100, false, 0)
	tc1.Close()

	// Allow time separation
	time.Sleep(10 * time.Millisecond)

	t.Setenv("EDR_SESSION", "")
	tc2 := NewCollector(dir, "test-1.0")
	if tc2 == nil {
		t.Fatal("expected non-nil collector")
	}
	cb2 := tc2.BeginCall()
	cb2.SetRequest(1, 0, 0, 0, 0, false, false, nil)
	cb2.Finish(50, false, 0)
	tc2.Close()

	// Open DB and score the target session explicitly
	db, err := OpenTraceDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	result, err := BenchSession(db, "target-session")
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalReads != 3 {
		t.Errorf("expected 3 reads from target session, got %d", result.TotalReads)
	}
	if result.SessionID != "target-session" {
		t.Errorf("SessionID = %q, want %q", result.SessionID, "target-session")
	}
}
