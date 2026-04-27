// Package session provides context-aware response optimization for edr.
// It tracks what content the caller has already seen and produces deltas
// and body dedup — the same logic for CLI and batch.
//
// Sessions are identified by the EDR_SESSION env var. When set, session state
// is persisted to ~/.edr/repos/<key>/sessions/<id>.json between calls. When unset, sessions
// are ephemeral (in-memory only, no persistence). Batch responses include a
// hint when no session is active so agents know to set one up.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"github.com/jordw/edr/internal/cmdspec"
)

type Session struct {
	mu            sync.Mutex
	FileContent   map[string]ContentEntry `json:"file_content"`
	SymbolContent map[string]ContentEntry `json:"symbol_content"`
	ContentOrder  int                     `json:"-"`
	SeenBodies    map[string]string       `json:"seen_bodies"`
	Diffs         map[string]string       `json:"diffs"`
	FileHashes map[string]string `json:"file_hashes,omitempty"`

	// Op log: sliding window of recent operations for `edr next`.
	OpLog   []OpEntry `json:"op_log,omitempty"`
	opCount int       // running counter for op ID generation (not persisted)

	// Focus is an optional one-line session objective shown by `edr next`.
	Focus string `json:"focus,omitempty"`

	// Assumptions tracks signature hashes for symbols the agent has read.
	Assumptions map[string]AssumptionEntry `json:"assumptions,omitempty"`

	// RunHashes tracks output hashes for command runs, enabling session-aware dedup.
	RunHashes map[string]string `json:"run_hashes,omitempty"`

	// Build state: tracks verify results and edit activity.
	LastVerifyStatus string `json:"last_verify_status,omitempty"`
	EditsSinceVerify bool   `json:"edits_since_verify,omitempty"`

	// FileMtimes tracks mtime+hash for files the agent has read, enabling
	// external modification detection via the warnings package.
	FileMtimes map[string]FileMtimeEntry `json:"file_mtimes,omitempty"`

	// ActiveTxn, when non-empty, names a cp_txn_* checkpoint that is the
	// rollback anchor for an open transaction. While set, pre-op checkpointing
	// appends to this checkpoint instead of creating rolling auto-checkpoints.
	ActiveTxn string `json:"active_txn,omitempty"`

	// repoRoot is the absolute path to the repo root, set at load time.
	// Used for stat fallback when mtime is not in the result map.
	repoRoot string

	// stats tracks optimization hits per handleDo call (reset between calls).
	stats PostProcessStats
}

// New creates an in-memory session.
func New() *Session {
	return &Session{
		FileContent:   make(map[string]ContentEntry),
		SymbolContent: make(map[string]ContentEntry),
		SeenBodies:    make(map[string]string),
		Diffs:         make(map[string]string),
		FileHashes:    make(map[string]string),
		FileMtimes:    make(map[string]FileMtimeEntry),
	}
}

// ResetStats resets per-call optimization counters.
func (s *Session) ResetStats() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats = PostProcessStats{}
}

// GetStats returns current optimization counters.
func (s *Session) GetStats() (deltaReads, bodyDedup int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats.DeltaReads, s.stats.BodyDedup
}

// SetRepoRoot sets the absolute repo root path for mtime stat fallback.
func (s *Session) SetRepoRoot(root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repoRoot = root
}

// RepoRoot returns the repo root path.
func (s *Session) RepoRoot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.repoRoot
}

// --- Hashing ---

func ContentHash(data string) string {
	h := sha256.Sum256([]byte(data))
	return hex.EncodeToString(h[:16])
}

// --- Command category maps — derived from the canonical registry in cmdspec. ---

var ReadCommands = deriveSet(cmdspec.IsRead)
var EditCommands = deriveSet(cmdspec.ModifiesState)
var DiffEditCommands = func() map[string]bool {
	m := deriveSet(cmdspec.IsDiffEdit)
	return m
}()
var DeltaReadCommands = deriveSet(cmdspec.IsDeltaRead)
var BodyCommands = deriveSet(cmdspec.IsBodyTrack)

func deriveSet(pred func(string) bool) map[string]bool {
	m := make(map[string]bool)
	for _, name := range cmdspec.Names() {
		if pred(name) {
			m[name] = true
		}
	}
	return m
}
