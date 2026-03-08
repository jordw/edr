package edit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
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

// Commit applies all queued edits. Edits are grouped by file and sorted in
// reverse byte-offset order so that earlier offsets remain valid as later spans
// are replaced. If any edit fails the function returns immediately — already-
// applied files are NOT rolled back; callers should use version control for
// recovery.
func (t *Transaction) Commit() error {
	// Group edits by file.
	grouped := make(map[string][]pendingEdit)
	for _, e := range t.edits {
		grouped[e.File] = append(grouped[e.File], e)
	}

	for file, edits := range grouped {
		// Sort in reverse order by StartByte (last first) to preserve offsets.
		sort.Slice(edits, func(i, j int) bool {
			return edits[i].StartByte > edits[j].StartByte
		})

		// Single read-modify-write per file to avoid race conditions.
		data, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("transaction: read %s: %w", file, err)
		}

		// Verify hash from the first edit (before any modifications).
		// Hashes are truncated to 8 hex chars, matching FileHash/ReplaceSpan.
		if h := edits[0].ExpectHash; h != "" {
			sum := sha256.Sum256(data)
			got := hex.EncodeToString(sum[:])[:8]
			if got != h {
				return fmt.Errorf("transaction: hash mismatch on %s: expected %s, got %s", file, h, got)
			}
		}

		// Apply all edits in-memory in reverse byte order (offsets stay valid).
		for _, e := range edits {
			if int(e.StartByte) > len(data) || int(e.EndByte) > len(data) || e.StartByte > e.EndByte {
				return fmt.Errorf("transaction: span [%d:%d] out of range for %s (len %d)", e.StartByte, e.EndByte, file, len(data))
			}
			data = append(data[:e.StartByte], append([]byte(e.Replacement), data[e.EndByte:]...)...)
		}

		// Preserve original file mode.
		fi, err := os.Stat(file)
		if err != nil {
			return fmt.Errorf("transaction: stat %s: %w", file, err)
		}
		if err := os.WriteFile(file, data, fi.Mode().Perm()); err != nil {
			return fmt.Errorf("transaction: write %s: %w", file, err)
		}
	}

	return nil
}

// Preview returns a copy of all queued edits.
func (t *Transaction) Preview() []pendingEdit {
	out := make([]pendingEdit, len(t.edits))
	copy(out, t.edits)
	return out
}
