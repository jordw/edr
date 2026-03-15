package trace

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jordw/edr/internal/session"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    edr_version TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS calls (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    seq INTEGER NOT NULL,
    started_at TEXT NOT NULL,
    duration_ms INTEGER NOT NULL,

    num_reads INTEGER DEFAULT 0,
    num_queries INTEGER DEFAULT 0,
    num_edits INTEGER DEFAULT 0,
    num_writes INTEGER DEFAULT 0,
    num_renames INTEGER DEFAULT 0,
    has_verify INTEGER DEFAULT 0,
    has_init INTEGER DEFAULT 0,
    budget_requested INTEGER,

    response_bytes INTEGER NOT NULL,
    num_warnings INTEGER DEFAULT 0,
    was_truncated INTEGER DEFAULT 0,

    num_delta_reads INTEGER DEFAULT 0,
    num_body_dedup INTEGER DEFAULT 0,
    num_slim_edits INTEGER DEFAULT 0,

    request_json TEXT,
    response_summary TEXT
);

CREATE TABLE IF NOT EXISTS edit_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    call_id INTEGER NOT NULL REFERENCES calls(id),
    file TEXT NOT NULL,
    lines_changed INTEGER,
    hash_before TEXT,
    hash_after TEXT,
    edit_ok INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS verify_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    call_id INTEGER NOT NULL REFERENCES calls(id),
    command TEXT NOT NULL,
    ok INTEGER NOT NULL,
    duration_ms INTEGER,
    output_bytes INTEGER
);

CREATE TABLE IF NOT EXISTS query_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    call_id INTEGER NOT NULL REFERENCES calls(id),
    cmd TEXT NOT NULL,
    ok INTEGER NOT NULL,
    result_bytes INTEGER
);
`

// traceEvent is the internal message sent from CallBuilder to the flush goroutine.
type traceEvent struct {
	call         callRecord
	editEvents   []EditEvent
	verifyEvents []VerifyEvent
	queryEvents  []QueryEvent
}

type callRecord struct {
	seq            int32
	startedAt      time.Time
	durationMs     int64
	numReads       int
	numQueries     int
	numEdits       int
	numWrites      int
	numRenames     int
	hasVerify      bool
	hasInit        bool
	budgetReq      *int
	responseBytes  int
	numWarnings    int
	wasTruncated   bool
	numDeltaReads  int
	numBodyDedup   int
	numSlimEdits   int
}

// EditEvent records one edit within a call.
type EditEvent struct {
	File         string
	LinesChanged int
	HashBefore   string
	HashAfter    string
	OK           bool
}

// VerifyEvent records a verify run within a call.
type VerifyEvent struct {
	Command    string
	OK         bool
	DurationMs int
	OutputBytes int
}

// QueryEvent records a query within a call.
type QueryEvent struct {
	Cmd        string
	OK         bool
	ResultBytes int
}

// Collector manages trace collection for a session.
type Collector struct {
	db        *sql.DB
	sessionID string
	seq       atomic.Int32
	ch        chan traceEvent
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// NewCollector opens (or creates) traces.db, inserts a session row, and starts the flush goroutine.
// Returns nil if trace DB cannot be opened (non-fatal).
func NewCollector(edrDir, version string) *Collector {
	dbPath := filepath.Join(edrDir, "traces.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "edr: trace db open: %v\n", err)
		return nil
	}

	// WAL mode for non-blocking appends
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA busy_timeout=3000")
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		fmt.Fprintf(os.Stderr, "edr: trace schema: %v\n", err)
		db.Close()
		return nil
	}

	sessionID := session.ResolveSessionID()
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = db.Exec("INSERT OR IGNORE INTO sessions (id, started_at, edr_version) VALUES (?, ?, ?)",
		sessionID, now, version)

	tc := &Collector{
		db:        db,
		sessionID: sessionID,
		ch:        make(chan traceEvent, 64),
	}
	tc.wg.Add(1)
	go tc.flushLoop()
	return tc
}

// Close sets ended_at, drains the channel, and closes the DB.
func (tc *Collector) Close() {
	if tc == nil {
		return
	}
	tc.closeOnce.Do(func() {
		close(tc.ch)
		tc.wg.Wait()

		now := time.Now().UTC().Format(time.RFC3339)
		tc.db.Exec("UPDATE sessions SET ended_at = ? WHERE id = ?", now, tc.sessionID)
		tc.db.Close()
	})
}

// BeginCall starts tracking a new tool call. Returns a CallBuilder.
// Safe to call on nil Collector (returns nil CallBuilder).
func (tc *Collector) BeginCall() *CallBuilder {
	if tc == nil {
		return nil
	}
	seq := tc.seq.Add(1)
	return &CallBuilder{
		tc:        tc,
		startedAt: time.Now(),
		call:      callRecord{seq: seq, startedAt: time.Now()},
	}
}

func (tc *Collector) flushLoop() {
	defer tc.wg.Done()
	for ev := range tc.ch {
		tc.flushEvent(ev)
	}
}

func (tc *Collector) flushEvent(ev traceEvent) {
	c := ev.call
	hasBudget := c.budgetReq != nil
	var budgetVal any
	if hasBudget {
		budgetVal = *c.budgetReq
	}

	hasVerify := 0
	if c.hasVerify {
		hasVerify = 1
	}
	hasInit := 0
	if c.hasInit {
		hasInit = 1
	}
	wasTruncated := 0
	if c.wasTruncated {
		wasTruncated = 1
	}

	res, err := tc.db.Exec(`INSERT INTO calls (
		session_id, seq, started_at, duration_ms,
		num_reads, num_queries, num_edits, num_writes, num_renames,
		has_verify, has_init, budget_requested,
		response_bytes, num_warnings, was_truncated,
		num_delta_reads, num_body_dedup, num_slim_edits
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tc.sessionID, c.seq, c.startedAt.UTC().Format(time.RFC3339), c.durationMs,
		c.numReads, c.numQueries, c.numEdits, c.numWrites, c.numRenames,
		hasVerify, hasInit, budgetVal,
		c.responseBytes, c.numWarnings, wasTruncated,
		c.numDeltaReads, c.numBodyDedup, c.numSlimEdits,
	)
	if err != nil {
		// Silently drop — stderr noise poisons agent context
		return
	}

	callID, err := res.LastInsertId()
	if err != nil {
		return
	}

	for _, e := range ev.editEvents {
		editOK := 0
		if e.OK {
			editOK = 1
		}
		tc.db.Exec("INSERT INTO edit_events (call_id, file, lines_changed, hash_before, hash_after, edit_ok) VALUES (?, ?, ?, ?, ?, ?)",
			callID, e.File, e.LinesChanged, e.HashBefore, e.HashAfter, editOK)
	}

	for _, v := range ev.verifyEvents {
		verifyOK := 0
		if v.OK {
			verifyOK = 1
		}
		tc.db.Exec("INSERT INTO verify_events (call_id, command, ok, duration_ms, output_bytes) VALUES (?, ?, ?, ?, ?)",
			callID, v.Command, verifyOK, v.DurationMs, v.OutputBytes)
	}

	for _, q := range ev.queryEvents {
		queryOK := 0
		if q.OK {
			queryOK = 1
		}
		tc.db.Exec("INSERT INTO query_events (call_id, cmd, ok, result_bytes) VALUES (?, ?, ?, ?)",
			callID, q.Cmd, queryOK, q.ResultBytes)
	}
}

// CallBuilder collects metrics during one handleDo call.
// All methods are no-ops on nil receiver.
type CallBuilder struct {
	tc           *Collector
	startedAt    time.Time
	call         callRecord
	editEvents   []EditEvent
	verifyEvents []VerifyEvent
	queryEvents  []QueryEvent
}

// SetRequest records the request shape.
func (cb *CallBuilder) SetRequest(numReads, numQueries, numEdits, numWrites, numRenames int, hasVerify, hasInit bool, budget *int) {
	if cb == nil {
		return
	}
	cb.call.numReads = numReads
	cb.call.numQueries = numQueries
	cb.call.numEdits = numEdits
	cb.call.numWrites = numWrites
	cb.call.numRenames = numRenames
	cb.call.hasVerify = hasVerify
	cb.call.hasInit = hasInit
	cb.call.budgetReq = budget
}

// AddEditEvent records an edit result.
func (cb *CallBuilder) AddEditEvent(file string, linesChanged int, hashBefore, hashAfter string, ok bool) {
	if cb == nil {
		return
	}
	cb.editEvents = append(cb.editEvents, EditEvent{
		File: file, LinesChanged: linesChanged,
		HashBefore: hashBefore, HashAfter: hashAfter, OK: ok,
	})
}

// AddVerifyEvent records a verify result.
func (cb *CallBuilder) AddVerifyEvent(command string, ok bool, durationMs, outputBytes int) {
	if cb == nil {
		return
	}
	cb.verifyEvents = append(cb.verifyEvents, VerifyEvent{
		Command: command, OK: ok, DurationMs: durationMs, OutputBytes: outputBytes,
	})
}

// AddQueryEvent records a query result.
func (cb *CallBuilder) AddQueryEvent(cmd string, ok bool, resultBytes int) {
	if cb == nil {
		return
	}
	cb.queryEvents = append(cb.queryEvents, QueryEvent{
		Cmd: cmd, OK: ok, ResultBytes: resultBytes,
	})
}

// SetSessionStats records session optimization hits.
func (cb *CallBuilder) SetSessionStats(deltaReads, bodyDedup, slimEdits int) {
	if cb == nil {
		return
	}
	cb.call.numDeltaReads = deltaReads
	cb.call.numBodyDedup = bodyDedup
	cb.call.numSlimEdits = slimEdits
}

// Finish calculates duration and sends the event to the flush channel.
// Non-blocking: drops the event if the channel is full.
func (cb *CallBuilder) Finish(responseBytes int, wasTruncated bool, numWarnings int) {
	if cb == nil {
		return
	}
	cb.call.durationMs = time.Since(cb.startedAt).Milliseconds()
	cb.call.responseBytes = responseBytes
	cb.call.wasTruncated = wasTruncated
	cb.call.numWarnings = numWarnings

	ev := traceEvent{
		call:         cb.call,
		editEvents:   cb.editEvents,
		verifyEvents: cb.verifyEvents,
		queryEvents:  cb.queryEvents,
	}

	// Non-blocking send
	select {
	case cb.tc.ch <- ev:
	default:
		// Silently drop — stderr noise poisons agent context
	}
}

// SessionID returns the current session ID.
func (tc *Collector) SessionID() string {
	if tc == nil {
		return ""
	}
	return tc.sessionID
}

// OpenTraceDB opens the trace database for reading (used by bench-session).
func OpenTraceDB(edrDir string) (*sql.DB, error) {
	dbPath := filepath.Join(edrDir, "traces.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.Exec("PRAGMA busy_timeout=3000")
	db.SetMaxOpenConns(1)
	return db, nil
}

// BenchResult contains scoring metrics for a session.
type BenchResult struct {
	SessionID     string  `json:"session_id"`
	StartedAt     string  `json:"started_at"`
	EndedAt       string  `json:"ended_at,omitempty"`
	EDRVersion    string  `json:"edr_version"`
	DurationSec   float64 `json:"duration_sec,omitempty"`

	TotalCalls     int `json:"total_calls"`
	TotalTokensEst int `json:"total_tokens_est"`

	TotalReads    int `json:"total_reads"`
	TotalQueries  int `json:"total_queries"`
	TotalEdits    int `json:"total_edits"`
	TotalWrites   int `json:"total_writes"`
	TotalRenames  int `json:"total_renames"`
	TotalVerifies int `json:"total_verifies"`

	DeltaReads int `json:"delta_reads"`
	BodyDedup  int `json:"body_dedup"`
	SlimEdits  int `json:"slim_edits"`

	EditFiles  int `json:"edit_files"`
	EditOK     int `json:"edits_ok"`
	EditFailed int `json:"edits_failed"`

	VerifyOK     int `json:"verify_ok"`
	VerifyFailed int `json:"verify_failed"`

	Truncations int `json:"truncations"`
	Warnings    int `json:"warnings"`

	// Derived analysis scores
	ReadEfficiency    *float64 `json:"read_efficiency,omitempty"`    // delta hits / total reads (higher = more reuse)
	EditSuccessRate   *float64 `json:"edit_success_rate,omitempty"`  // ok edits / total edit events
	VerifyPassRate    *float64 `json:"verify_pass_rate,omitempty"`   // ok verifies / total verify events
	OptimizationRate  *float64 `json:"optimization_rate,omitempty"`  // (delta+dedup+slim) / total calls
	TokensPerCall     *float64 `json:"tokens_per_call,omitempty"`    // avg tokens per call
	EditsReverted     int      `json:"edits_reverted"`               // files where final hash == first hash_before (wasted edits)
	AvgCallDurationMs *float64 `json:"avg_call_duration_ms,omitempty"`
}

// BenchSession scores a session. If sessionID is empty, uses the most recent session.
func BenchSession(db *sql.DB, sessionID string) (*BenchResult, error) {
	if sessionID == "" {
		err := db.QueryRow("SELECT id FROM sessions ORDER BY rowid DESC LIMIT 1").Scan(&sessionID)
		if err != nil {
			return nil, fmt.Errorf("no sessions found: %w", err)
		}
	}

	r := &BenchResult{SessionID: sessionID}

	// Session metadata
	err := db.QueryRow("SELECT started_at, COALESCE(ended_at, ''), edr_version FROM sessions WHERE id = ?",
		sessionID).Scan(&r.StartedAt, &r.EndedAt, &r.EDRVersion)
	if err != nil {
		return nil, fmt.Errorf("session %s not found: %w", sessionID, err)
	}

	// Calculate duration
	if r.EndedAt != "" {
		start, _ := time.Parse(time.RFC3339, r.StartedAt)
		end, _ := time.Parse(time.RFC3339, r.EndedAt)
		if !start.IsZero() && !end.IsZero() {
			r.DurationSec = end.Sub(start).Seconds()
		}
	}

	// Aggregate call metrics
	row := db.QueryRow(`SELECT
		COUNT(*),
		COALESCE(SUM(response_bytes), 0),
		COALESCE(SUM(num_reads), 0),
		COALESCE(SUM(num_queries), 0),
		COALESCE(SUM(num_edits), 0),
		COALESCE(SUM(num_writes), 0),
		COALESCE(SUM(num_renames), 0),
		COALESCE(SUM(has_verify), 0),
		COALESCE(SUM(num_delta_reads), 0),
		COALESCE(SUM(num_body_dedup), 0),
		COALESCE(SUM(num_slim_edits), 0),
		COALESCE(SUM(was_truncated), 0),
		COALESCE(SUM(num_warnings), 0)
	FROM calls WHERE session_id = ?`, sessionID)

	var totalBytes int
	err = row.Scan(
		&r.TotalCalls, &totalBytes,
		&r.TotalReads, &r.TotalQueries, &r.TotalEdits, &r.TotalWrites, &r.TotalRenames,
		&r.TotalVerifies,
		&r.DeltaReads, &r.BodyDedup, &r.SlimEdits,
		&r.Truncations, &r.Warnings,
	)
	if err != nil {
		return nil, err
	}
	r.TotalTokensEst = totalBytes / 4

	// Edit events
	db.QueryRow(`SELECT
		COUNT(DISTINCT file), COALESCE(SUM(CASE WHEN edit_ok=1 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN edit_ok=0 THEN 1 ELSE 0 END), 0)
	FROM edit_events WHERE call_id IN (SELECT id FROM calls WHERE session_id = ?)`, sessionID).
		Scan(&r.EditFiles, &r.EditOK, &r.EditFailed)

	// Verify events
	db.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN ok=1 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN ok=0 THEN 1 ELSE 0 END), 0)
	FROM verify_events WHERE call_id IN (SELECT id FROM calls WHERE session_id = ?)`, sessionID).
		Scan(&r.VerifyOK, &r.VerifyFailed)

	// Derived: read efficiency (delta hits / total reads)
	if r.TotalReads > 0 {
		v := float64(r.DeltaReads) / float64(r.TotalReads)
		r.ReadEfficiency = &v
	}

	// Derived: edit success rate
	totalEditEvents := r.EditOK + r.EditFailed
	if totalEditEvents > 0 {
		v := float64(r.EditOK) / float64(totalEditEvents)
		r.EditSuccessRate = &v
	}

	// Derived: verify pass rate
	totalVerifyEvents := r.VerifyOK + r.VerifyFailed
	if totalVerifyEvents > 0 {
		v := float64(r.VerifyOK) / float64(totalVerifyEvents)
		r.VerifyPassRate = &v
	}

	// Derived: optimization rate (how often session optimizations fire)
	if r.TotalCalls > 0 {
		totalOpts := r.DeltaReads + r.BodyDedup + r.SlimEdits
		v := float64(totalOpts) / float64(r.TotalCalls)
		r.OptimizationRate = &v
	}

	// Derived: tokens per call
	if r.TotalCalls > 0 {
		v := float64(r.TotalTokensEst) / float64(r.TotalCalls)
		r.TokensPerCall = &v
	}

	// Derived: avg call duration
	var avgDur sql.NullFloat64
	db.QueryRow("SELECT AVG(duration_ms) FROM calls WHERE session_id = ?", sessionID).Scan(&avgDur)
	if avgDur.Valid {
		r.AvgCallDurationMs = &avgDur.Float64
	}

	// Derived: edits reverted — files where the last hash_after == first hash_before
	// (meaning the net effect was zero, the edit was undone)
	rows, err := db.Query(`
		SELECT file, MIN(call_id) as first_call, MAX(call_id) as last_call
		FROM edit_events
		WHERE call_id IN (SELECT id FROM calls WHERE session_id = ?) AND edit_ok = 1
		GROUP BY file
		HAVING COUNT(*) > 1`, sessionID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var file string
			var firstCall, lastCall int64
			if rows.Scan(&file, &firstCall, &lastCall) != nil {
				continue
			}
			var firstBefore, lastAfter sql.NullString
			db.QueryRow("SELECT hash_before FROM edit_events WHERE call_id = ? AND file = ? ORDER BY id LIMIT 1",
				firstCall, file).Scan(&firstBefore)
			db.QueryRow("SELECT hash_after FROM edit_events WHERE call_id = ? AND file = ? ORDER BY id DESC LIMIT 1",
				lastCall, file).Scan(&lastAfter)
			if firstBefore.Valid && lastAfter.Valid && firstBefore.String != "" && firstBefore.String == lastAfter.String {
				r.EditsReverted++
			}
		}
	}

	return r, nil
}

// BenchResultJSON returns the bench result as formatted JSON.
func BenchResultJSON(r *BenchResult) string {
	data, _ := json.MarshalIndent(r, "", "  ")
	return string(data)
}
