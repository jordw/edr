package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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

const MaxContentEntries = 200

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

// --- Cache keys ---

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
		// Preserve expand data (callers/deps) even on unchanged reads.
		for _, key := range []string{"callers", "callers_method", "deps", "deps_method"} {
			if v, ok := result[key]; ok {
				deduped[key] = v
			}
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
