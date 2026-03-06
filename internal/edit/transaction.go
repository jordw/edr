package edit

import (
	"fmt"
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

		for idx, e := range edits {
			// Only pass the expectHash on the first edit applied to this file
			// (before any modifications). After the first edit the hash will
			// have changed.
			hash := ""
			if idx == 0 {
				hash = e.ExpectHash
			}
			if err := ReplaceSpan(file, e.StartByte, e.EndByte, e.Replacement, hash); err != nil {
				return fmt.Errorf("transaction: commit failed on %s: %w", file, err)
			}
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
