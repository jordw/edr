package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const sessionTTL = 30 * time.Minute

// sessionFile is the on-disk format with a timestamp for TTL.
type sessionFile struct {
	UpdatedAt     time.Time               `json:"updated_at"`
	Responses     map[string]string       `json:"responses"`
	Diffs         map[string]string       `json:"diffs"`
	FileContent   map[string]ContentEntry `json:"file_content"`
	SymbolContent map[string]ContentEntry `json:"symbol_content"`
	ContentOrder  int                     `json:"content_order"`
	SeenBodies    map[string]string       `json:"seen_bodies"`
}

// NewFileSession loads or creates a PPID-scoped session file in .edr/sessions/.
func NewFileSession(repoRoot string) (*Session, error) {
	ppid := os.Getppid()
	dir := filepath.Join(repoRoot, ".edr", "sessions")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}

	// Clean up stale sessions
	cleanStaleSessions(dir)

	fp := filepath.Join(dir, fmt.Sprintf("%d.json", ppid))
	s := New()
	s.filePath = fp

	data, err := os.ReadFile(fp)
	if err != nil {
		// No existing session — return fresh
		return s, nil
	}

	var sf sessionFile
	if err := json.Unmarshal(data, &sf); err != nil {
		// Corrupt file — return fresh
		return s, nil
	}

	// Check TTL
	if time.Since(sf.UpdatedAt) > sessionTTL {
		os.Remove(fp)
		return s, nil
	}

	// Restore state
	s.Responses = sf.Responses
	s.Diffs = sf.Diffs
	s.FileContent = sf.FileContent
	s.SymbolContent = sf.SymbolContent
	s.ContentOrder = sf.ContentOrder
	s.SeenBodies = sf.SeenBodies
	return s, nil
}

// Save persists the session to disk. No-op for in-memory sessions.
func (s *Session) Save() error {
	if s.filePath == "" {
		return nil
	}

	sf := sessionFile{
		UpdatedAt:     time.Now(),
		Responses:     s.Responses,
		Diffs:         s.Diffs,
		FileContent:   s.FileContent,
		SymbolContent: s.SymbolContent,
		ContentOrder:  s.ContentOrder,
		SeenBodies:    s.SeenBodies,
	}

	data, err := json.Marshal(sf)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	// Atomic write via temp file
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write session: %w", err)
	}
	return os.Rename(tmp, s.filePath)
}

// cleanStaleSessions removes session files older than the TTL.
func cleanStaleSessions(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	now := time.Now()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > sessionTTL {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
