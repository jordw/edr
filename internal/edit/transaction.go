package edit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// pendingEdit represents a queued edit that has not yet been applied.
type pendingEdit struct {
	File        string
	StartByte   uint32
	EndByte     uint32
	Replacement string
	ExpectHash  string
}

// Transaction collects multiple edits and applies them atomically per file.
type Transaction struct {
	edits []pendingEdit
}

// NewTransaction returns a new empty Transaction.
func NewTransaction() *Transaction {
	return &Transaction{}
}

// Add queues an edit to be applied when Commit is called. If expectHash is
// non-empty, the file's hash will be verified before any edits for that file
// are applied.
func (t *Transaction) Add(file string, startByte, endByte uint32, replacement, expectHash string) {
	t.edits = append(t.edits, pendingEdit{
		File:        file,
		StartByte:   startByte,
		EndByte:     endByte,
		Replacement: replacement,
		ExpectHash:  expectHash,
	})
}

// Commit applies all queued edits atomically. Edits are grouped by file and
// sorted in reverse byte-offset order so that earlier offsets remain valid as
// later spans are replaced. All files are validated and transformed in memory
// first; disk writes use write-to-temp-then-rename with rollback on failure.
func (t *Transaction) Commit() error {
	// Group edits by file.
	grouped := make(map[string][]pendingEdit)
	for _, e := range t.edits {
		grouped[e.File] = append(grouped[e.File], e)
	}

	// Phase 1: Read all files, validate hashes and spans, compute new contents.
	type fileResult struct {
		data []byte
		mode os.FileMode
	}
	results := make(map[string]fileResult, len(grouped))

	for file, edits := range grouped {
		// Sort in reverse order by StartByte (last first) to preserve offsets.
		sort.Slice(edits, func(i, j int) bool {
			return edits[i].StartByte > edits[j].StartByte
		})

		data, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("transaction: read %s: %w", file, err)
		}

		// Verify hash from ALL edits that carry one (not just the first).
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])[:8]
		for _, e := range edits {
			if e.ExpectHash != "" && e.ExpectHash != got {
				return fmt.Errorf("transaction: hash mismatch on %s: expected %s, got %s", file, e.ExpectHash, got)
			}
		}

		// Validate and apply all edits in-memory in reverse byte order.
		for _, e := range edits {
			if int(e.StartByte) > len(data) || int(e.EndByte) > len(data) || e.StartByte > e.EndByte {
				return fmt.Errorf("transaction: span [%d:%d] out of range for %s (len %d)", e.StartByte, e.EndByte, file, len(data))
			}
			data = append(data[:e.StartByte], append([]byte(e.Replacement), data[e.EndByte:]...)...)
		}

		fi, err := os.Stat(file)
		if err != nil {
			return fmt.Errorf("transaction: stat %s: %w", file, err)
		}
		results[file] = fileResult{data: data, mode: fi.Mode().Perm()}
	}

	// Phase 2: Write all files atomically using temp files + rename.
	// Track successfully written files for rollback on failure.
	type written struct {
		file    string
		backup  string // temp file holding original content
	}
	var completed []written

	rollback := func() {
		for _, w := range completed {
			// Restore original content from backup
			if orig, err := os.ReadFile(w.backup); err == nil {
				os.WriteFile(w.file, orig, 0644)
			}
			os.Remove(w.backup)
		}
	}

	for file, res := range results {
		// Save original content to a temp file for rollback
		origData, err := os.ReadFile(file)
		if err != nil {
			rollback()
			return fmt.Errorf("transaction: re-read %s for backup: %w", file, err)
		}
		backupFile, err := os.CreateTemp(filepath.Dir(file), ".edr-backup-*")
		if err != nil {
			rollback()
			return fmt.Errorf("transaction: create backup for %s: %w", file, err)
		}
		if _, err := backupFile.Write(origData); err != nil {
			backupFile.Close()
			os.Remove(backupFile.Name())
			rollback()
			return fmt.Errorf("transaction: write backup for %s: %w", file, err)
		}
		backupFile.Close()

		// Write new content via temp file + rename for crash safety
		tmpFile, err := os.CreateTemp(filepath.Dir(file), ".edr-edit-*")
		if err != nil {
			os.Remove(backupFile.Name())
			rollback()
			return fmt.Errorf("transaction: create temp for %s: %w", file, err)
		}
		if _, err := tmpFile.Write(res.data); err != nil {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			os.Remove(backupFile.Name())
			rollback()
			return fmt.Errorf("transaction: write temp for %s: %w", file, err)
		}
		tmpFile.Close()

		if err := os.Chmod(tmpFile.Name(), res.mode); err != nil {
			os.Remove(tmpFile.Name())
			os.Remove(backupFile.Name())
			rollback()
			return fmt.Errorf("transaction: chmod temp for %s: %w", file, err)
		}
		if err := os.Rename(tmpFile.Name(), file); err != nil {
			os.Remove(tmpFile.Name())
			os.Remove(backupFile.Name())
			rollback()
			return fmt.Errorf("transaction: rename temp to %s: %w", file, err)
		}

		completed = append(completed, written{file: file, backup: backupFile.Name()})
	}

	// Phase 3: Clean up backup files (all writes succeeded).
	for _, w := range completed {
		os.Remove(w.backup)
	}

	return nil
}

// Preview returns a copy of all queued edits.
func (t *Transaction) Preview() []pendingEdit {
	out := make([]pendingEdit, len(t.edits))
	copy(out, t.edits)
	return out
}
