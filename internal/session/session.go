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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jordw/edr/internal/cmdspec"
)

// ContentEntry tracks previously sent content for delta reads.
// Only the hash is stored (not content) to keep session files small.
type ContentEntry struct {
	Hash  string `json:"hash"`
	Order int    `json:"order"`
}

// PostProcessStats tracks session optimization hits for trace collection.
type PostProcessStats struct {
	DeltaReads int
	BodyDedup  int
}

type Session struct {
	mu            sync.Mutex
	FileContent   map[string]ContentEntry `json:"file_content"`
	SymbolContent map[string]ContentEntry `json:"symbol_content"`
	ContentOrder  int                     `json:"content_order"`
	SeenBodies    map[string]string       `json:"seen_bodies"`
	Diffs         map[string]string       `json:"diffs"`

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
func (s *Session) GetStats() (deltaReads, bodyDedup, slimEdits int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats.DeltaReads, s.stats.BodyDedup, 0
}

const MaxContentEntries = 200

// New creates an in-memory session.
func New() *Session {
	return &Session{
		FileContent:   make(map[string]ContentEntry),
		SymbolContent: make(map[string]ContentEntry),
		SeenBodies:    make(map[string]string),
		Diffs:         make(map[string]string),
	}
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
	return s
}

// SaveToFile persists the session to disk. Uses atomic write (tmp + rename).
func (s *Session) SaveToFile(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.Marshal(s)
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ResolveSessionID returns the session ID from the EDR_SESSION env var.
// Returns "" when unset, which means no session (ephemeral, no dedup).
// Agents must explicitly set EDR_SESSION to opt into session features.
func ResolveSessionID() string {
	return os.Getenv("EDR_SESSION")
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
	return sess, func() {
		sess.SaveToFile(path)
	}
}

// Command category maps — derived from the canonical registry in cmdspec.
var ReadCommands = deriveSet(cmdspec.IsRead)
var EditCommands = deriveSet(cmdspec.ModifiesState)
var DiffEditCommands = func() map[string]bool {
	m := deriveSet(cmdspec.IsDiffEdit)
	m["edit-plan"] = true // internal batch edit implementation
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
	if cmd == "rename" || cmd == "reindex" {
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
	if FlagIsTruthy(flags, "full") {
		s.StoreReadContent(cmd, result)
		return nil
	}

	var content, key string
	var isSymbol bool

	// Detect whether this is a file read or symbol read by result shape
	if c, ok := result["body"].(string); ok && c != "" {
		content = c
		isSymbol = true
		sym, _ := result["symbol"].(map[string]any)
		if sym == nil {
			return nil
		}
		file, _ := sym["file"].(string)
		name, _ := sym["name"].(string)
		key = file + ":" + name
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
		s.SeenBodies[file+":"+name] = ContentHash(content)
	} else if c, ok := result["content"].(string); ok && c != "" {
		content = c
		file, _ := result["file"].(string)
		lines, _ := result["lines"]
		key = fmt.Sprintf("%s:%v", file, lines)
		if b, ok := flags["budget"]; ok {
			if bi, isNum := b.(float64); isNum && bi > 0 {
				key += fmt.Sprintf(":budget=%d", int(bi))
			} else if bi, isInt := b.(int); isInt && bi > 0 {
				key += fmt.Sprintf(":budget=%d", bi)
			}
		}
	} else {
		return nil
	}

	status, _ := s.CheckContent(key, content, isSymbol)

	switch status {
	case "new":
		s.StoreContent(key, content, isSymbol)
		result["session"] = "new"
		return nil

	case "unchanged":
		s.stats.DeltaReads++
		file, hash := ExtractFileHash(result)
		return map[string]any{"unchanged": true, "file": file, "hash": hash, "session": "unchanged"}
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
	if c, ok := result["body"].(string); ok && c != "" {
		sym, _ := result["symbol"].(map[string]any)
		if sym != nil {
			file, _ := sym["file"].(string)
			name, _ := sym["name"].(string)
			key := file + ":" + name
			s.StoreContent(key, c, true)
			s.SeenBodies[key] = ContentHash(c)
		}
	} else if c, ok := result["content"].(string); ok && c != "" {
		file, _ := result["file"].(string)
		lines, _ := result["lines"]
		key := fmt.Sprintf("%s:%v", file, lines)
		s.StoreContent(key, c, false)
	}
}

// --- Level 3: Body tracking ---

func (s *Session) TrackBodies(result map[string]any, cmd string) {
	if body, ok := result["body"].(string); ok && body != "" {
		sym, _ := result["symbol"].(map[string]any)
		if sym != nil {
			file, _ := sym["file"].(string)
			name, _ := sym["name"].(string)
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
			return `{"session":"unchanged"}`
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
			lines, _ := entry["lines"]
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
