// Package session provides context-aware response optimization for edr.
// It tracks what content the caller has already seen and produces deltas,
// slim edits, and body dedup — the same logic for CLI and batch.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/jordw/edr/internal/cmdspec"
	"github.com/jordw/edr/internal/edit"
)

// ContentEntry tracks previously sent content for delta reads.
type ContentEntry struct {
	Hash    string `json:"hash"`
	Content string `json:"content"`
	Order   int    `json:"order"`
}

// Session tracks content the caller has already seen.
// It powers four optimizations:
//  1. Response-level dedup: identical read → {cached:true}
//  2. Slim edits: strip large diffs, serve via GetDiff
//  3. Delta reads: re-reads return a diff from the last-seen version
//  4. Seen-body stripping: gather/search skip bodies already in context
// PostProcessStats tracks session optimization hits for trace collection.
type PostProcessStats struct {
	DeltaReads int
	BodyDedup  int
	SlimEdits  int
}

type Session struct {
	mu            sync.Mutex
	Responses     map[string]string       `json:"responses"`
	Diffs         map[string]string       `json:"diffs"`
	FileContent   map[string]ContentEntry `json:"file_content"`
	SymbolContent map[string]ContentEntry `json:"symbol_content"`
	ContentOrder  int                     `json:"content_order"`
	SeenBodies    map[string]string       `json:"seen_bodies"`

	// filePath is set for file-backed sessions. Empty = in-memory only.
	filePath string

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
	return s.stats.DeltaReads, s.stats.BodyDedup, s.stats.SlimEdits
}

const MaxContentEntries = 200

// New creates an in-memory session.
func New() *Session {
	return &Session{
		Responses:     make(map[string]string),
		Diffs:         make(map[string]string),
		FileContent:   make(map[string]ContentEntry),
		SymbolContent: make(map[string]ContentEntry),
		SeenBodies:    make(map[string]string),
	}
}

// Command category maps — derived from the canonical registry in cmdspec.
// Exported for backward compatibility; prefer cmdspec helpers in new code.
var ReadCommands = deriveSet(cmdspec.IsRead)
var EditCommands = deriveSet(cmdspec.ModifiesState)
var DiffEditCommands = deriveSet(cmdspec.IsDiffEdit)
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
	for _, f := range []string{"budget", "body", "callers", "deps", "depth", "signatures", "context", "regex", "include", "exclude", "dir", "glob", "type", "grep", "symbols", "full", "verbose"} {
		if v, ok := flags[f]; ok {
			key += fmt.Sprintf("\x00%s=%v", f, v)
		}
	}
	return key
}

// Check returns true if this response was already sent identically.
func (s *Session) Check(key, responseText string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := ContentHash(responseText)
	if prev, ok := s.Responses[key]; ok && prev == h {
		return true
	}
	s.Responses[key] = h
	return false
}

// --- Cache invalidation ---

func (s *Session) InvalidateFile(file string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidateFile(file)
}

func (s *Session) invalidateFile(file string) {
	for k := range s.Responses {
		if strings.Contains(k, file) {
			delete(s.Responses, k)
		}
	}
}

func (s *Session) InvalidateForEdit(cmd string, args []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cmd == "rename" || cmd == "init" {
		s.Responses = make(map[string]string)
		s.Diffs = make(map[string]string)
		s.FileContent = make(map[string]ContentEntry)
		s.SymbolContent = make(map[string]ContentEntry)
		s.SeenBodies = make(map[string]string)
		return
	}
	if len(args) > 0 {
		s.invalidateFile(args[0])
	}
}

// --- Level 1: Slim edit responses ---

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

// StoreDiff stores the diff from an edit result and returns a slimmed version.
// Small diffs (<=20 changed lines) are included inline. Large diffs are stored
// and available via GetDiff. Returns nil if verbose is set.
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
	if FlagIsTruthy(flags, "verbose") {
		return nil
	}

	file, _ := result["file"].(string)
	// edit-plan results have no top-level "file" — infer from hashes map.
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

	if changedLines <= 20 {
		result["lines_changed"] = changedLines
		result["diff_available"] = true
		return nil
	}

	s.stats.SlimEdits++
	slim := make(map[string]any)
	for k, v := range result {
		if k == "diff" || k == "old_size" || k == "new_size" {
			continue
		}
		slim[k] = v
	}
	slim["lines_changed"] = changedLines
	slim["diff_available"] = true
	return slim
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
	return map[string]any{"error": "no diff stored for " + key + "; diffs are session-scoped and only available for files edited during this session. Use queries: [{cmd: \"search\", pattern: \"...\", text: true}] or git diff instead.", "key": key}
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
		Hash:    ContentHash(content),
		Content: content,
		Order:   s.ContentOrder,
	}
	if isSymbol {
		s.SymbolContent[key] = entry
	} else {
		s.FileContent[key] = entry
	}
	s.evictLRU()
}

// CheckContent checks if content has been seen before.
// Returns: "new", "unchanged", or "changed" (with old content and previous hash).
func (s *Session) CheckContent(key string, content string, isSymbol bool) (status string, oldContent string, prevHash string) {
	var store map[string]ContentEntry
	if isSymbol {
		store = s.SymbolContent
	} else {
		store = s.FileContent
	}

	prev, exists := store[key]
	if !exists {
		return "new", "", ""
	}

	h := ContentHash(content)
	if prev.Hash == h {
		s.ContentOrder++
		prev.Order = s.ContentOrder
		store[key] = prev
		return "unchanged", "", prev.Hash
	}
	return "changed", prev.Content, prev.Hash
}

// --- Text diff ---

func ComputeTextDiff(oldText, newText, label string) string {
	if oldText == newText {
		return ""
	}
	if len(strings.Split(oldText, "\n")) > 2000 || len(strings.Split(newText, "\n")) > 2000 {
		return ""
	}
	return edit.UnifiedDiff(label, []byte(oldText), []byte(newText))
}

// --- Level 2: Process read results ---

func (s *Session) ProcessReadResult(cmd string, result map[string]any, flags map[string]any) map[string]any {
	if FlagIsTruthy(flags, "full") {
		s.StoreReadContent(cmd, result)
		return nil
	}

	var content, key, label string
	var isSymbol bool

	// Detect whether this is a file read or symbol read by result shape
	if c, ok := result["body"].(string); ok && c != "" {
		// Symbol-shaped result (read symbol, explore)
		content = c
		isSymbol = true
		sym, _ := result["symbol"].(map[string]any)
		if sym == nil {
			return nil
		}
		file, _ := sym["file"].(string)
		name, _ := sym["name"].(string)
		key = file + ":" + name
		// Depth/budget-specific reads produce different content for the same symbol;
		// track them under separate keys so different truncation levels don't
		// produce spurious deltas.
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
		label = key
		s.SeenBodies[file+":"+name] = ContentHash(content)
	} else if c, ok := result["content"].(string); ok && c != "" {
		// File-shaped result (read file)
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
		label = file
	} else {
		return nil
	}

	status, oldContent, prevHash := s.CheckContent(key, content, isSymbol)

	switch status {
	case "new":
		s.StoreContent(key, content, isSymbol)
		return nil

	case "unchanged":
		s.stats.DeltaReads++
		file, hash := ExtractFileHash(result)
		return map[string]any{"unchanged": true, "file": file, "hash": hash}

	case "changed":
		diff := ComputeTextDiff(oldContent, content, label)
		s.StoreContent(key, content, isSymbol)
		if diff == "" {
			return nil
		}
		// If the old content was much smaller (e.g. signatures→full body),
		// the delta is a near-complete rewrite that wastes tokens vs. just
		// returning the full response. Skip delta when <25% overlap.
		if len(oldContent)*4 < len(content) {
			return nil
		}
		s.stats.DeltaReads++
		file, hash := ExtractFileHash(result)
		return map[string]any{
			"delta":         true,
			"file":          file,
			"diff":          diff,
			"hash":          hash,
			"previous_hash": prevHash,
			"new_size":      len(content) / 4,
		}
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
	// Symbol body (read symbol, explore)
	if body, ok := result["body"].(string); ok && body != "" {
		sym, _ := result["symbol"].(map[string]any)
		if sym != nil {
			file, _ := sym["file"].(string)
			name, _ := sym["name"].(string)
			s.SeenBodies[file+":"+name] = ContentHash(body)
		}
	}
	// Gather target body
	if body, ok := result["target_body"].(string); ok && body != "" {
		if target, ok := result["target"].(map[string]any); ok {
			file, _ := target["file"].(string)
			name, _ := target["name"].(string)
			s.SeenBodies[file+":"+name] = ContentHash(body)
		}
	}
	// Search matches
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
	case "gather", "explore":
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

	// Level 1: Slim edit responses
	if cmdspec.IsDiffEdit(cmd) {
		if slim := s.storeDiff(m, flags); slim != nil {
			data, _ := json.Marshal(slim)
			return string(data)
		} else if m["diff_available"] == true {
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
	}

	// Level 3: Strip seen bodies from gather/search.
	// StripSeenBodies handles both stripping previously-seen bodies and
	// tracking new ones, so we must NOT call TrackBodies first for these
	// commands (that would mark current results as "seen" before stripping).
	willStrip := cmd == "gather" || (cmd == "search" && FlagIsTruthy(flags, "body")) || (cmd == "explore" && FlagIsTruthy(flags, "gather") && FlagIsTruthy(flags, "body"))
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

// postProcessNonObject is the lock-free implementation. Caller must hold s.mu.
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

		status, oldContent, prevHash := s.CheckContent(key, content, isSymbol)
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
		case "changed":
			label := file
			if symbol != "" {
				label = file + ":" + symbol
			}
			diff := ComputeTextDiff(oldContent, content, label)
			s.StoreContent(key, content, isSymbol)
			if diff != "" {
				entries[i] = map[string]any{
					"delta":         true,
					"file":          file,
					"diff":          diff,
					"hash":          entry["hash"],
					"previous_hash": prevHash,
					"new_size":      len(content) / 4,
				}
				if symbol != "" {
					entries[i]["symbol"] = symbol
				}
				modified = true
			}
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
