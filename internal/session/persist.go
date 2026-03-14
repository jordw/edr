package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Open loads a file-backed session from disk. If the file doesn't exist,
// it creates a new session that will persist on Save.
func Open(path string) (*Session, error) {
	s := New()
	s.filePath = path

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("session open: %w", err)
	}

	if err := json.Unmarshal(data, s); err != nil {
		// Corrupt file — start fresh rather than failing.
		s2 := New()
		s2.filePath = path
		return s2, nil
	}
	s.filePath = path
	return s, nil
}

// Save persists the session state to disk. No-op for in-memory sessions.
func (s *Session) Save() error {
	if s.filePath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Enforce LRU cap before saving.
	evictLRU(s.FileContent, MaxContentEntries)
	evictLRU(s.SymbolContent, MaxContentEntries)

	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("session save: %w", err)
	}

	// Atomic write: write to temp, then rename.
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("session save mkdir: %w", err)
	}
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("session save write: %w", err)
	}
	if err := os.Rename(tmp, s.filePath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("session save rename: %w", err)
	}
	return nil
}

// Close saves and clears the file path (idempotent).
func (s *Session) Close() error {
	err := s.Save()
	s.filePath = ""
	return err
}

// evictLRU removes the oldest entries until the map is within cap.
// Collects all entries, sorts by order, and bulk-deletes in one pass.
func evictLRU(m map[string]ContentEntry, cap int) {
	if len(m) <= cap {
		return
	}
	type kv struct {
		key   string
		order int
	}
	entries := make([]kv, 0, len(m))
	for k, v := range m {
		entries = append(entries, kv{k, v.Order})
	}
	// Partial sort: find the (len-cap) smallest by order and delete them.
	// Simple approach: sort and delete the first (len-cap) entries.
	sort.Slice(entries, func(i, j int) bool { return entries[i].order < entries[j].order })
	for i := 0; i < len(entries)-cap; i++ {
		delete(m, entries[i].key)
	}
}

// SessionDir returns the sessions directory for a given edr root.
func SessionDir(edrDir string) string {
	return filepath.Join(edrDir, "sessions")
}

// SessionPath returns the file path for a session token.
// Returns an error if the token contains path traversal characters.
func SessionPath(edrDir, token string) (string, error) {
	if token == "" || strings.ContainsAny(token, "/\\") || strings.Contains(token, "..") {
		return "", fmt.Errorf("invalid session token: %q", token)
	}
	return filepath.Join(SessionDir(edrDir), token+".json"), nil
}

// ListSessions returns all session tokens in the sessions directory.
func ListSessions(edrDir string) ([]string, error) {
	dir := SessionDir(edrDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var tokens []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if ext := filepath.Ext(name); ext == ".json" {
			tokens = append(tokens, name[:len(name)-len(ext)])
		}
	}
	return tokens, nil
}

// ClearSession deletes a session file.
func ClearSession(edrDir, token string) error {
	path, err := SessionPath(edrDir, token)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// GCSessions deletes sessions whose tokens are numeric PIDs that are no longer running.
func GCSessions(edrDir string) ([]string, error) {
	tokens, err := ListSessions(edrDir)
	if err != nil {
		return nil, err
	}
	var cleared []string
	for _, tok := range tokens {
		pid, err := strconv.Atoi(tok)
		if err != nil {
			continue // not a PID-based token
		}
		if !processAlive(pid) {
			if err := ClearSession(edrDir, tok); err == nil {
				cleared = append(cleared, tok)
			}
		}
	}
	return cleared, nil
}

// processAlive checks if a PID is still running.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, signal 0 checks existence without sending a signal.
	return proc.Signal(syscall.Signal(0)) == nil
}
