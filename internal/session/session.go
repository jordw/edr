// Package session provides context-aware response optimization for edr.
// It tracks what content the caller has already seen and produces deltas
// and body dedup — the same logic for CLI and batch.
//
// Sessions are identified by the EDR_SESSION env var. When set, session state
// is persisted to .edr/sessions/<id>.json between calls. When unset, sessions
// are ephemeral (in-memory only, no persistence). Batch responses include a
// hint when no session is active so agents know to set one up.
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jordw/edr/internal/cmdspec"
)

// ContentEntry tracks previously sent content for delta reads.
// Only the hash is stored (not content) to keep session files small.
type ContentEntry struct {
	Hash  string `json:"hash"`
	Order int    `json:"order"`
}

// FileMtimeEntry tracks a file's mtime and content hash at the time the agent
// last read it. Used by the warnings package to detect external modifications.
type FileMtimeEntry struct {
	Mtime int64  `json:"mtime"` // UnixMicro
	Hash  string `json:"hash"`  // content hash at read time
	OpID  string `json:"op_id"` // op when this was recorded
}

// PostProcessStats tracks session optimization hits for trace collection.
type PostProcessStats struct {
	DeltaReads int
	BodyDedup  int
}

// OpEntry records a single operation in the session op log.
// Used by `edr next` to show recent activity for re-orientation.
type OpEntry struct {
	OpID   string `json:"op_id"`   // "e7", "r3", "s1"
	Cmd    string `json:"cmd"`     // "edit", "read", "search"
	File   string `json:"file"`    // relative path
	Symbol string `json:"symbol"`  // symbol name, if any
	Action string `json:"action"`  // raw operation: "replace_text", "delete", "insert_at", "read_symbol"
	Kind   string `json:"kind"`    // display label: "signature_changed", "text_replaced", "symbol_read"
	OK     bool   `json:"ok"`      // success/failure
}

// MaxOpLogEntries is the sliding window size for the op log.
const MaxOpLogEntries = 100

// AssumptionEntry tracks a signature snapshot for a symbol the agent has read.
type AssumptionEntry struct {
	SigHash string `json:"sig_hash"` // SHA256 prefix of the signature string
	OpID    string `json:"op_id"`    // op ID when the assumption was recorded (e.g., "r3")
}

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

	// repoRoot is the absolute path to the repo root, set at load time.
	// Used for stat fallback when mtime is not in the result map.
	repoRoot string

	// stats tracks optimization hits per handleDo call (reset between calls).
	stats PostProcessStats
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

// --- Op log ---

func (s *Session) RecordOp(cmd, file, symbol, action, kind string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := "x"
	if len(cmd) > 0 {
		prefix = string(cmd[0])
	}
	s.opCount++
	opID := fmt.Sprintf("%s%d", prefix, s.opCount)
	s.OpLog = append(s.OpLog, OpEntry{OpID: opID, Cmd: cmd, File: file, Symbol: symbol, Action: action, Kind: kind, OK: ok})
	if len(s.OpLog) > MaxOpLogEntries {
		s.OpLog = s.OpLog[len(s.OpLog)-MaxOpLogEntries:]
	}
}

func (s *Session) GetRecentOps(n int) []OpEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 || n > len(s.OpLog) {
		out := make([]OpEntry, len(s.OpLog))
		copy(out, s.OpLog)
		return out
	}
	start := len(s.OpLog) - n
	out := make([]OpEntry, n)
	copy(out, s.OpLog[start:])
	return out
}

func (s *Session) GetFocus() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Focus
}

func (s *Session) SetFocus(focus string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Focus = focus
}

// --- Assumption tracking ---

func SigHash(sig string) string { return ContentHash(sig) }

func (s *Session) RecordAssumption(key, sig, opID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Assumptions == nil {
		s.Assumptions = make(map[string]AssumptionEntry)
	}
	s.Assumptions[key] = AssumptionEntry{SigHash: SigHash(sig), OpID: opID}
}

// UpdateAssumptionOpID updates just the op ID for an existing assumption.
func (s *Session) UpdateAssumptionOpID(key, opID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.Assumptions[key]; ok {
		entry.OpID = opID
		s.Assumptions[key] = entry
	}
}

type StaleAssumption struct {
	Key, File, Symbol, AssumedAt, Current string
}

func (s *Session) CheckAssumptions(currentSigs map[string]string) []StaleAssumption {
	s.mu.Lock()
	defer s.mu.Unlock()
	var stale []StaleAssumption
	for key, entry := range s.Assumptions {
		cur, ok := currentSigs[key]
		if !ok {
			stale = append(stale, StaleAssumption{Key: key, AssumedAt: entry.OpID})
			continue
		}
		if cur != entry.SigHash {
			stale = append(stale, StaleAssumption{Key: key, AssumedAt: entry.OpID, Current: cur})
		}
	}
	for i := range stale {
		if idx := strings.IndexByte(stale[i].Key, ':'); idx > 0 {
			stale[i].File = stale[i].Key[:idx]
			stale[i].Symbol = stale[i].Key[idx+1:]
		}
	}
	return stale
}

func (s *Session) GetAssumptions() map[string]AssumptionEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]AssumptionEntry, len(s.Assumptions))
	for k, v := range s.Assumptions {
		out[k] = v
	}
	return out
}

func (s *Session) ClearAssumption(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Assumptions, key)
}

// --- Build state ---

func (s *Session) RecordVerify(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastVerifyStatus = status
	s.EditsSinceVerify = false
}

func (s *Session) RecordEdit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.EditsSinceVerify = true
}

func (s *Session) BuildState() (status string, editsSince bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.LastVerifyStatus == "" {
		return "", false
	}
	if s.EditsSinceVerify {
		return "unknown", true
	}
	return s.LastVerifyStatus, false
}


// --- Run output caching ---

// CheckRunOutput checks whether command output matches the previously stored hash.
// Returns "unchanged" if the output is identical, "new" otherwise.
func (s *Session) CheckRunOutput(key string, output string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.RunHashes == nil {
		return "new"
	}
	prev, ok := s.RunHashes[key]
	if !ok {
		return "new"
	}
	if ContentHash(output) == prev {
		return "unchanged"
	}
	return "new"
}

// StoreRunOutput stores the output hash for a command run.
func (s *Session) StoreRunOutput(key string, output string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.RunHashes == nil {
		s.RunHashes = make(map[string]string)
	}
	s.RunHashes[key] = ContentHash(output)
}

// ClearRunOutput removes the stored hash for a command key.
func (s *Session) ClearRunOutput(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.RunHashes, key)
}

const MaxContentEntries = 200

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

// --- File mtime tracking ---

// GetFileMtimes returns a copy of the tracked file mtimes.
func (s *Session) GetFileMtimes() map[string]FileMtimeEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]FileMtimeEntry, len(s.FileMtimes))
	for k, v := range s.FileMtimes {
		out[k] = v
	}
	return out
}

// RecordFileMtime records the mtime and content hash for a file the agent read.
func (s *Session) RecordFileMtime(relPath string, mtime int64, hash string, opID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FileMtimes == nil {
		s.FileMtimes = make(map[string]FileMtimeEntry)
	}
	s.FileMtimes[relPath] = FileMtimeEntry{Mtime: mtime, Hash: hash, OpID: opID}
}

// UpdateFileMtime updates just the mtime for a tracked file (e.g., after
// detecting a touch that didn't change content).
func (s *Session) UpdateFileMtime(relPath string, mtime int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.FileMtimes[relPath]; ok {
		entry.Mtime = mtime
		s.FileMtimes[relPath] = entry
	}
}

// ClearFileMtime removes mtime tracking for a file.
func (s *Session) ClearFileMtime(relPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.FileMtimes, relPath)
}

// --- File-backed persistence ---
// LoadFromFile loads a session from disk. Returns a new empty session if the
// file does not exist or is corrupt.
func LoadFromFile(path string) *Session {
	data, err := os.ReadFile(path)
	if err != nil {
		return New()
	}
	s := New()
	if json.Unmarshal(data, s) != nil {
		return New()
	}
	// Ensure maps are non-nil after unmarshal
	if s.FileContent == nil {
		s.FileContent = make(map[string]ContentEntry)
	}
	if s.SymbolContent == nil {
		s.SymbolContent = make(map[string]ContentEntry)
	}
	if s.SeenBodies == nil {
		s.SeenBodies = make(map[string]string)
	}
	if s.Diffs == nil {
		s.Diffs = make(map[string]string)
	}
	if s.FileMtimes == nil {
		s.FileMtimes = make(map[string]FileMtimeEntry)
	}
	// Restore opCount from existing log so new IDs don't collide
	if len(s.OpLog) > 0 {
		last := s.OpLog[len(s.OpLog)-1]
		if len(last.OpID) > 1 {
			if n, err := strconv.Atoi(last.OpID[1:]); err == nil {
				s.opCount = n
			}
		}
	}
	return s
}

// SaveToFile persists the session to disk. Uses atomic write (tmp + rename).
func (s *Session) SaveToFile(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	data, err := json.Marshal(s)
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ResolveSessionID returns the session ID from the EDR_SESSION env var.
// Returns "" when unset, which means no session (ephemeral, no dedup).
// Agents must explicitly set EDR_SESSION to opt into session features.
func ResolveSessionID() string {
	id := os.Getenv("EDR_SESSION")
	if id != "" {
		return id
	}
	return resolveByPPID()
}

// stableAncestorPID finds the stable process that launched edr. It walks up
// the process tree from edr's parent looking for a shell (zsh, bash, sh,
// fish). If found, the shell's parent is the agent/terminal — which is
// stable across tool calls. If no shell is found (agent invokes edr directly),
// the direct parent is already the stable process.
func stableAncestorPID() int {
	pid := os.Getppid()
	name := processName(pid)
	if isShell(name) {
		if parent := parentPID(pid); parent > 1 {
			return parent
		}
	}
	return pid
}

var shells = map[string]bool{
	"zsh": true, "bash": true, "sh": true, "fish": true,
	"dash": true, "ksh": true, "csh": true, "tcsh": true,
	"-zsh": true, "-bash": true, "-sh": true, "-fish": true,
}

func isShell(name string) bool {
	return shells[name] || shells[filepath.Base(name)]
}

// processName returns the command name for a PID, or "" on error.
func processName(pid int) string {
	// Linux: /proc/<pid>/comm
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
		return strings.TrimSpace(string(data))
	}
	// macOS: ps -o comm= -p <pid>
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return filepath.Base(strings.TrimSpace(string(out)))
}

// parentPID returns the parent PID of a process, or 0 on error.
func parentPID(pid int) int {
	// Linux: /proc/<pid>/stat
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		s := string(data)
		if i := strings.LastIndex(s, ") "); i >= 0 {
			fields := strings.Fields(s[i+2:])
			if len(fields) >= 2 {
				if n, err := strconv.Atoi(fields[1]); err == nil {
					return n
				}
			}
		}
		return 0
	}
	// macOS: ps -o ppid= -p <pid>
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil {
		return n
	}
	return 0
}

func resolveByPPID() string {
	root, err := findRepoRoot()
	if err != nil {
		return ""
	}
	sessDir := filepath.Join(root, ".edr", "sessions")
	pid := stableAncestorPID()
	path := filepath.Join(sessDir, fmt.Sprintf("ppid_%d", pid))
	startTime := processStartTime(pid)

	// Read existing mapping (format: "session_id\nstart_time").
	// Validate that the process start time still matches to detect PID reuse.
	if data, err := os.ReadFile(path); err == nil {
		lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
		if len(lines) >= 1 && lines[0] != "" {
			id := lines[0]
			if len(lines) == 2 {
				// Validate start time — if it changed, PID was reused.
				if startTime != "" && startTime == lines[1] {
					return id
				}
				// Start time mismatch or unavailable — fall through to create new.
			} else {
				// Legacy format (no start time) — accept but upgrade on next write.
				return id
			}
		}
	}

	// No valid mapping — create a fresh session for this process.
	id := GenerateID()
	os.MkdirAll(sessDir, 0700)
	writePPIDMapping(path, id, startTime)
	return id
}

// processStartTime returns a stable string identifying when a process started.
// Used to detect PID reuse: if a PID's start time doesn't match what we
// recorded, the PID was recycled by the OS for a different process.
func processStartTime(pid int) string {
	// Linux: /proc/<pid>/stat field 22 (starttime in clock ticks)
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		s := string(data)
		if i := strings.LastIndex(s, ") "); i >= 0 {
			fields := strings.Fields(s[i+2:])
			if len(fields) >= 20 {
				return fields[19] // starttime
			}
		}
		return ""
	}
	// macOS: ps -o lstart= -p <pid> (e.g., "Sat Mar 22 19:30:00 2026")
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// writePPIDMapping writes a ppid mapping file with format "session_id\nstart_time".
func writePPIDMapping(path, id, startTime string) {
	content := id
	if startTime != "" {
		content = id + "\n" + startTime
	}
	os.WriteFile(path, []byte(content), 0600)
}

// WriteSessionMapping writes a PPID mapping file for the stable ancestor.
func WriteSessionMapping(sessDir, id string) {
	pid := stableAncestorPID()
	path := filepath.Join(sessDir, fmt.Sprintf("ppid_%d", pid))
	writePPIDMapping(path, id, processStartTime(pid))
}

// findRepoRoot walks up from cwd to find .edr directory.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".edr")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no .edr found")
		}
		dir = parent
	}
}

// GenerateID creates a short unique session ID (8 hex chars from timestamp + random).
func GenerateID() string {
	b := make([]byte, 4)
	// Use crypto/rand for uniqueness
	rand.Read(b)
	return hex.EncodeToString(b)
}

// LoadSession loads the session identified by EDR_SESSION env var.
// Returns the session and a save function. Call save() after processing
// to persist changes. If EDR_SESSION is not set, returns an ephemeral
// session and a no-op save.
func LoadSession(edrDir string) (*Session, func()) {
	id := ResolveSessionID()
	if id == "" {
		return New(), func() {}
	}
	path := filepath.Join(edrDir, "sessions", id+".json")
	sess := LoadFromFile(path)
	// Set repo root (edrDir is <repo>/.edr, root is parent)
	sess.repoRoot = filepath.Dir(edrDir)
	return sess, func() {
		sess.SaveToFile(path)
	}
}

// Command category maps — derived from the canonical registry in cmdspec.
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

// --- Hashing & keys ---

func ContentHash(data string) string {
	h := sha256.Sum256([]byte(data))
	return hex.EncodeToString(h[:16])
}

func (s *Session) CacheKey(cmd string, args []string, flags map[string]any) string {
	key := cmd + "\x00" + strings.Join(args, "\x00")
	for _, f := range []string{"budget", "body", "callers", "deps", "depth", "signatures", "context", "regex", "include", "exclude", "dir", "glob", "type", "grep", "symbols", "full", "verbose", "text", "impact", "chain", "limit", "no_group"} {
		if v, ok := flags[f]; ok {
			key += fmt.Sprintf("\x00%s=%v", f, v)
		}
	}
	return key
}

// --- Cache invalidation ---

func (s *Session) InvalidateFile(file string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidateFile(file)
}

func (s *Session) invalidateFile(file string) {
	for k := range s.FileContent {
		if strings.Contains(k, file) {
			delete(s.FileContent, k)
		}
	}
	for k := range s.SymbolContent {
		if strings.Contains(k, file) {
			delete(s.SymbolContent, k)
		}
	}
	for k := range s.Diffs {
		if strings.Contains(k, file) {
			delete(s.Diffs, k)
		}
	}
}

func (s *Session) InvalidateForEdit(cmd string, args []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cmd == "rename" {
		s.FileContent = make(map[string]ContentEntry)
		s.SymbolContent = make(map[string]ContentEntry)
		s.SeenBodies = make(map[string]string)
		s.Diffs = make(map[string]string)
		return
	}
	if len(args) > 0 {
		s.invalidateFile(args[0])
	}
}

// --- Level 1: Edit diff storage ---

func CountDiffLines(diff string) int {
	count := 0
	for _, line := range strings.Split(diff, "\n") {
		if len(line) == 0 {
			continue
		}
		if (line[0] == '+' || line[0] == '-') &&
			!strings.HasPrefix(line, "---") && !strings.HasPrefix(line, "+++") {
			count++
		}
	}
	return count
}

// StoreDiff stores the diff from an edit result.
// Diffs are always included inline; stored for later GetDiff retrieval.
func (s *Session) StoreDiff(result map[string]any, flags map[string]any) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.storeDiff(result, flags)
}

func (s *Session) storeDiff(result map[string]any, flags map[string]any) map[string]any {
	diff, ok := result["diff"].(string)
	if !ok || diff == "" {
		return nil
	}

	file, _ := result["file"].(string)
	if file == "" {
		if hashes, ok := result["hashes"].(map[string]any); ok && len(hashes) == 1 {
			for f := range hashes {
				file = f
			}
		}
	}
	key := file
	if sym, ok := result["symbol"].(string); ok && sym != "" {
		key = file + ":" + sym
	}
	s.Diffs[key] = diff

	changedLines := CountDiffLines(diff)
	result["lines_changed"] = changedLines
	result["diff_available"] = true
	return nil
}

// GetDiff returns a stored diff by file or file:symbol key.
func (s *Session) GetDiff(args []string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getDiff(args)
}

func (s *Session) getDiff(args []string) map[string]any {
	if len(args) == 0 {
		return map[string]any{"error": "get-diff requires 1-2 arguments: <file> [symbol]"}
	}
	key := args[0]
	if len(args) > 1 {
		key = args[0] + ":" + args[1]
	}
	if diff, ok := s.Diffs[key]; ok {
		return map[string]any{"diff": diff, "file": args[0]}
	}
	if len(args) > 1 {
		if diff, ok := s.Diffs[args[0]]; ok {
			return map[string]any{"diff": diff, "file": args[0]}
		}
	}
	return map[string]any{"error": "no diff stored for " + key, "key": key}
}

// --- Level 2: Delta reads ---

func (s *Session) evictLRU() {
	total := len(s.FileContent) + len(s.SymbolContent)
	if total <= MaxContentEntries {
		return
	}
	oldestKey := ""
	oldestOrder := s.ContentOrder + 1
	oldestIsFile := true

	for k, v := range s.FileContent {
		if v.Order < oldestOrder {
			oldestOrder = v.Order
			oldestKey = k
			oldestIsFile = true
		}
	}
	for k, v := range s.SymbolContent {
		if v.Order < oldestOrder {
			oldestOrder = v.Order
			oldestKey = k
			oldestIsFile = false
		}
	}
	if oldestKey != "" {
		if oldestIsFile {
			delete(s.FileContent, oldestKey)
		} else {
			delete(s.SymbolContent, oldestKey)
		}
	}
}

func (s *Session) StoreContent(key string, content string, isSymbol bool) {
	s.ContentOrder++
	entry := ContentEntry{
		Hash:  ContentHash(content),
		Order: s.ContentOrder,
	}
	if isSymbol {
		s.SymbolContent[key] = entry
	} else {
		s.FileContent[key] = entry
	}
	s.evictLRU()
}

// CheckContent checks if content has been seen before.
// Returns "new" or "unchanged". (No "changed" state — we only store hashes.)
func (s *Session) CheckContent(key string, content string, isSymbol bool) (status string, prevHash string) {
	var store map[string]ContentEntry
	if isSymbol {
		store = s.SymbolContent
	} else {
		store = s.FileContent
	}

	prev, exists := store[key]
	if !exists {
		return "new", ""
	}

	h := ContentHash(content)
	if prev.Hash == h {
		s.ContentOrder++
		prev.Order = s.ContentOrder
		store[key] = prev
		return "unchanged", prev.Hash
	}
	// Content changed — treat as new (we don't store old content for diffing)
	return "new", prev.Hash
}

// --- Level 2: Process read results ---

func (s *Session) ProcessReadResult(cmd string, result map[string]any, flags map[string]any) map[string]any {
	// Track file-level hash for stale-read protection on any read.
	// No lock needed: caller (PostProcess) already holds s.mu.
	if hash, ok := result["hash"].(string); ok && hash != "" {
		if file, ok := result["file"].(string); ok && file != "" {
			if s.FileHashes == nil {
				s.FileHashes = make(map[string]string)
			}
			s.FileHashes[file] = hash
			s.updateFileMtimeFromResult(file, hash, result)
		}
	}

	if FlagIsTruthy(flags, "full") {
		s.StoreReadContent(cmd, result)
		return nil
	}

	c, ok := result["content"].(string)
	if !ok || c == "" {
		return nil
	}
	content := c

	// Detect symbol read: "symbol" field is a string (symbol name)
	symName, isSymbol := result["symbol"].(string)
	file, _ := result["file"].(string)

	var key string
	if isSymbol && symName != "" {
		key = file + ":" + symName
		s.SeenBodies[key] = ContentHash(content)
	} else {
		lines := result["lines"]
		key = fmt.Sprintf("%s:%v", file, lines)
	}
	if d, ok := flags["depth"]; ok {
		if di, isNum := d.(float64); isNum && di > 0 {
			key += fmt.Sprintf(":depth=%d", int(di))
		} else if di, isInt := d.(int); isInt && di > 0 {
			key += fmt.Sprintf(":depth=%d", di)
		}
	}
	if b, ok := flags["budget"]; ok {
		if bi, isNum := b.(float64); isNum && bi > 0 {
			key += fmt.Sprintf(":budget=%d", int(bi))
		} else if bi, isInt := b.(int); isInt && bi > 0 {
			key += fmt.Sprintf(":budget=%d", bi)
		}
	}

	status, _ := s.CheckContent(key, content, isSymbol)

	switch status {
	case "new":
		s.StoreContent(key, content, isSymbol)
		result["session"] = "new"
		return nil

	case "unchanged":
		s.stats.DeltaReads++
		if isSymbol {
			// Symbol reads are small — always emit content to avoid a --full round-trip.
			result["session"] = "unchanged"
			return nil
		}
		file, hash := ExtractFileHash(result)
		deduped := map[string]any{"unchanged": true, "file": file, "hash": hash, "session": "unchanged", "hint": "use --full to force re-read"}
		if v, ok := result["lines"]; ok {
			deduped["lines"] = v
		}
		if v, ok := result["sym"]; ok {
			deduped["sym"] = v
		}
		return deduped
	}
	return nil
}

// ExtractFileHash gets file and hash from a result map.
func ExtractFileHash(result map[string]any) (file, hash string) {
	file, _ = result["file"].(string)
	hash, _ = result["hash"].(string)
	if file == "" {
		if sym, ok := result["symbol"].(map[string]any); ok {
			file, _ = sym["file"].(string)
			hash, _ = sym["hash"].(string)
		}
	}
	return
}

// StoreReadContent stores content from a read result for future delta tracking.
func (s *Session) StoreReadContent(cmd string, result map[string]any) {
	c, ok := result["content"].(string)
	if !ok || c == "" {
		return
	}
	file, _ := result["file"].(string)
	if symName, ok := result["symbol"].(string); ok && symName != "" {
		key := file + ":" + symName
		s.StoreContent(key, c, true)
		s.SeenBodies[key] = ContentHash(c)
	} else {
		lines := result["lines"]
		key := fmt.Sprintf("%s:%v", file, lines)
		s.StoreContent(key, c, false)
	}
}

// CheckFileHash returns the stored hash for a file, or "" if no read has been recorded.
func (s *Session) CheckFileHash(file string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FileHashes == nil {
		return ""
	}
	return s.FileHashes[file]
}

// RefreshFileHash updates the stored hash for a file to the given value.
// Used to recover from hash mismatches caused by external modifications.
func (s *Session) RefreshFileHash(file, hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FileHashes == nil {
		s.FileHashes = make(map[string]string)
	}
	s.FileHashes[file] = hash
}

// updateFileHashFromResult extracts file+hash from an edit result and updates
// FileHashes so subsequent edits use the post-edit hash, not a stale read hash.
// Caller must hold s.mu.
func (s *Session) updateFileHashFromResult(m map[string]any) {
	file, hash := ExtractFileHash(m)
	if file != "" && hash != "" {
		if s.FileHashes == nil {
			s.FileHashes = make(map[string]string)
		}
		s.FileHashes[file] = hash
		s.updateFileMtimeFromResult(file, hash, m)
	}
}

// updateFileMtimeFromResult updates mtime tracking after a read or edit.
// It tries the "mtime" field in the result first, then falls back to stat.
// Caller must hold s.mu.
func (s *Session) updateFileMtimeFromResult(file, hash string, m map[string]any) {
	var unixMicro int64

	if mtimeStr, ok := m["mtime"].(string); ok && mtimeStr != "" {
		if t, err := time.Parse(time.RFC3339, mtimeStr); err == nil {
			unixMicro = t.UnixMicro()
		}
	}

	// Fallback: stat the file using RepoRoot if set.
	if unixMicro == 0 && s.repoRoot != "" {
		absPath := file
		if !strings.HasPrefix(file, "/") {
			absPath = filepath.Join(s.repoRoot, file)
		}
		if info, err := os.Stat(absPath); err == nil {
			unixMicro = info.ModTime().UnixMicro()
		}
	}

	if unixMicro == 0 {
		return
	}

	if s.FileMtimes == nil {
		s.FileMtimes = make(map[string]FileMtimeEntry)
	}
	opID := ""
	if len(s.OpLog) > 0 {
		opID = s.OpLog[len(s.OpLog)-1].OpID
	}
	s.FileMtimes[file] = FileMtimeEntry{
		Mtime: unixMicro,
		Hash:  hash,
		OpID:  opID,
	}

}

// firstString returns the first non-empty string value found for the given keys.
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// --- Level 3: Body tracking ---

func (s *Session) TrackBodies(result map[string]any, cmd string) {
	// Track top-level content — check both "content" (read results) and "body" (gather/refs)
	if body := firstString(result, "content", "body"); body != "" {
		file, _ := result["file"].(string)
		name := ""
		if s, ok := result["symbol"].(string); ok {
			name = s
		} else if sym, ok := result["symbol"].(map[string]any); ok {
			file, _ = sym["file"].(string)
			name, _ = sym["name"].(string)
		}
		if name != "" {
			s.SeenBodies[file+":"+name] = ContentHash(body)
		}
	}
	if body, ok := result["target_body"].(string); ok && body != "" {
		if target, ok := result["target"].(map[string]any); ok {
			file, _ := target["file"].(string)
			name, _ := target["name"].(string)
			s.SeenBodies[file+":"+name] = ContentHash(body)
		}
	}
	if matchesAny, ok := result["matches"]; ok {
		if matches, ok := matchesAny.([]any); ok {
			for _, mAny := range matches {
				m, ok := mAny.(map[string]any)
				if !ok {
					continue
				}
				body, ok := m["body"].(string)
				if !ok || body == "" {
					continue
				}
				sym, _ := m["symbol"].(map[string]any)
				if sym == nil {
					continue
				}
				file, _ := sym["file"].(string)
				name, _ := sym["name"].(string)
				s.SeenBodies[file+":"+name] = ContentHash(body)
			}
		}
	}
}

func (s *Session) StripSeenBodies(result map[string]any, cmd string) {
	var skipped []string

	switch cmd {
	case "gather", "refs":
		if body, ok := result["target_body"].(string); ok && body != "" {
			if target, ok := result["target"].(map[string]any); ok {
				file, _ := target["file"].(string)
				name, _ := target["name"].(string)
				key := file + ":" + name
				h := ContentHash(body)
				if prev, exists := s.SeenBodies[key]; exists && prev == h {
					result["target_body"] = "[in context]"
					skipped = append(skipped, name)
				} else {
					s.SeenBodies[key] = h
				}
			}
		}
		s.stripSnippetMap(result, "caller_snippets", &skipped)
		s.stripSnippetMap(result, "test_snippets", &skipped)

	case "search":
		if matchesAny, ok := result["matches"]; ok {
			if matches, ok := matchesAny.([]any); ok {
				for _, mAny := range matches {
					m, ok := mAny.(map[string]any)
					if !ok {
						continue
					}
					body, ok := m["body"].(string)
					if !ok || body == "" {
						continue
					}
					sym, _ := m["symbol"].(map[string]any)
					if sym == nil {
						continue
					}
					file, _ := sym["file"].(string)
					name, _ := sym["name"].(string)
					key := file + ":" + name
					h := ContentHash(body)
					if prev, exists := s.SeenBodies[key]; exists && prev == h {
						m["body"] = "[in context]"
						skipped = append(skipped, name)
					} else {
						s.SeenBodies[key] = h
					}
				}
			}
		}
	}

	if len(skipped) > 0 {
		s.stats.BodyDedup += len(skipped)
		result["skipped_bodies"] = skipped
	}
}

func (s *Session) stripSnippetMap(result map[string]any, field string, skipped *[]string) {
	snippets, ok := result[field].(map[string]any)
	if !ok {
		return
	}
	for name, bodyAny := range snippets {
		body, ok := bodyAny.(string)
		if !ok || body == "" {
			continue
		}
		if s.isBodySeen(name, body) {
			snippets[name] = "[in context]"
			*skipped = append(*skipped, name)
		} else {
			s.trackBodyByName(name, body)
		}
	}
}

// IsBlockSeen checks if a block hash has been seen in this session.
func (s *Session) IsBlockSeen(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.SeenBodies[key]
	return ok
}

// MarkBlockSeen records a block hash as seen.
func (s *Session) MarkBlockSeen(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.SeenBodies[key] = "1"
}

func (s *Session) isBodySeen(name, body string) bool {
	h := ContentHash(body)
	for key, prevHash := range s.SeenBodies {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) == 2 && parts[1] == name && prevHash == h {
			return true
		}
	}
	return false
}

func (s *Session) trackBodyByName(name, body string) {
	h := ContentHash(body)
	for key := range s.SeenBodies {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) == 2 && parts[1] == name {
			s.SeenBodies[key] = h
			return
		}
	}
	s.SeenBodies[":"+name] = h
}

// --- Post-processing pipeline ---

// PostProcess applies all session-layer optimizations to a dispatch result.
func (s *Session) PostProcess(cmd string, args []string, flags map[string]any, result any, text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		return s.postProcessNonObject(cmd, args, flags, text)
	}

	// Level 1: Store edit diffs (always inline, no slim optimization)
	if DiffEditCommands[cmd] {
		s.storeDiff(m, flags)
		// Update FileHashes so the next edit uses the post-edit hash,
		// not the stale hash from the prior read.
		s.updateFileHashFromResult(m)
		if m["diff_available"] == true {
			data, _ := json.Marshal(m)
			return string(data)
		}
	}

	// Level 2: Delta reads
	if cmdspec.IsDeltaRead(cmd) {
		if delta := s.ProcessReadResult(cmd, m, flags); delta != nil {
			data, _ := json.Marshal(delta)
			return string(data)
		}
		// ProcessReadResult may have added "session" field to m
		if _, has := m["session"]; has {
			data, _ := json.Marshal(m)
			text = string(data)
		}
	}

	// Level 2b: Content-hash session tracking for search/map/refs
	// Hash the visible payload and return "unchanged" if agent already has it.
	if cmd == "search" || cmd == "map" || cmd == "refs" {
		cacheKey := s.CacheKey(cmd, args, flags)
		status, _ := s.CheckContent(cacheKey, text, false)
		if status == "unchanged" {
			s.stats.DeltaReads++
			// Mark as unchanged but preserve the full body — search/map/refs
			// results are already budget-capped and agents need to re-reference
			// results after context compression.
			m["session"] = "unchanged"
			data, _ := json.Marshal(m)
			return string(data)
		}
		s.StoreContent(cacheKey, text, false)
		m["session"] = "new"
		data, _ := json.Marshal(m)
		text = string(data)
	}

	// Level 3: Strip seen bodies from gather/search.
	willStrip := cmd == "gather" || (cmd == "search" && FlagIsTruthy(flags, "body")) || (cmd == "refs" && FlagIsTruthy(flags, "body"))
	if cmdspec.IsBodyTrack(cmd) && !willStrip {
		s.TrackBodies(m, cmd)
	}
	if willStrip {
		s.StripSeenBodies(m, cmd)
		data, _ := json.Marshal(m)
		return string(data)
	}

	return text
}

// PostProcessNonObject handles array results (batch read).
func (s *Session) PostProcessNonObject(cmd string, args []string, flags map[string]any, text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.postProcessNonObject(cmd, args, flags, text)
}

func (s *Session) postProcessNonObject(cmd string, args []string, flags map[string]any, text string) string {
	if cmd != "read" {
		return text
	}

	isFull := FlagIsTruthy(flags, "full")

	var entries []map[string]any
	if err := json.Unmarshal([]byte(text), &entries); err != nil {
		return text
	}

	modified := false
	for i, entry := range entries {
		content, ok := entry["content"].(string)
		if !ok || content == "" {
			continue
		}

		file, _ := entry["file"].(string)
		symbol, _ := entry["symbol"].(string)

		var key string
		var isSymbol bool
		if symbol != "" {
			key = file + ":" + symbol
			isSymbol = true
			s.SeenBodies[key] = ContentHash(content)
		} else {
			lines := entry["lines"]
			key = fmt.Sprintf("%s:%v", file, lines)
		}

		if isFull {
			s.StoreContent(key, content, isSymbol)
			continue
		}

		status, _ := s.CheckContent(key, content, isSymbol)
		switch status {
		case "new":
			s.StoreContent(key, content, isSymbol)
		case "unchanged":
			hash, _ := entry["hash"].(string)
			entries[i] = map[string]any{"unchanged": true, "file": file, "hash": hash}
			if symbol != "" {
				entries[i]["symbol"] = symbol
			}
			modified = true
		}
	}

	if modified {
		data, _ := json.Marshal(entries)
		return string(data)
	}
	return text
}

// FlagIsTruthy checks if a flag value is boolean true.
func FlagIsTruthy(flags map[string]any, key string) bool {
	v, ok := flags[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}
