package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

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
