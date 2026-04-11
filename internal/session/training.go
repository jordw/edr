package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// TrainingLabel records a shortlist presentation + the user's subsequent pick.
type TrainingLabel struct {
	Query      string           `json:"query"`
	Repo       string           `json:"repo"`
	Candidates []TrainingCandidate `json:"candidates"`
	Label      int              `json:"label"` // index of chosen candidate (-1 if no pick)
}

// TrainingCandidate is a shortlist candidate with enough info for training.
type TrainingCandidate struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	File      string `json:"file"`
	StartLine uint32 `json:"start_line"`
	EndLine   uint32 `json:"end_line"`
}

// PendingShortlist is a shortlist waiting for the user to pick a candidate.
type PendingShortlist struct {
	Query      string
	Candidates []TrainingCandidate
}

var (
	pendingMu        sync.Mutex
	pendingShortlist *PendingShortlist
)

// RecordShortlist saves a shortlist presentation so the next focus can be
// matched as a training label. Persists to disk so it survives across
// separate edr invocations.
func RecordShortlist(query string, candidates []TrainingCandidate) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	pendingShortlist = &PendingShortlist{
		Query:      query,
		Candidates: candidates,
	}
}

// RecordShortlistPersist saves the shortlist to disk for cross-process matching.
func RecordShortlistPersist(edrDir, query string, candidates []TrainingCandidate) {
	RecordShortlist(query, candidates)
	if edrDir == "" {
		return
	}
	pending := PendingShortlist{Query: query, Candidates: candidates}
	data, err := json.Marshal(pending)
	if err != nil {
		return
	}
	os.WriteFile(filepath.Join(edrDir, "pending_shortlist.json"), data, 0644)
}

// MatchPick checks if a focus command matches a pending shortlist candidate.
// If so, writes a training label to the log and clears the pending shortlist.
// Checks both in-memory and on-disk pending shortlists.
// Returns true if a label was written.
func MatchPick(edrDir, repoRoot, file, symbol string) bool {
	pendingMu.Lock()
	pending := pendingShortlist
	pendingShortlist = nil // consume regardless
	pendingMu.Unlock()

	// Try disk if not in memory (cross-process)
	if pending == nil && edrDir != "" {
		path := filepath.Join(edrDir, "pending_shortlist.json")
		data, err := os.ReadFile(path)
		if err == nil {
			var p PendingShortlist
			if json.Unmarshal(data, &p) == nil && p.Query != "" {
				pending = &p
			}
		}
		os.Remove(path) // consume regardless
	}

	if pending == nil || file == "" {
		return false
	}

	// Normalize file to relative path
	rel := file
	if filepath.IsAbs(file) && repoRoot != "" {
		if r, err := filepath.Rel(repoRoot, file); err == nil {
			rel = r
		}
	}

	// Find matching candidate
	chosenIdx := -1
	for i, c := range pending.Candidates {
		if c.File == rel && strings.EqualFold(c.Name, symbol) {
			chosenIdx = i
			break
		}
	}

	if chosenIdx < 0 {
		return false
	}

	// Write label
	label := TrainingLabel{
		Query:      pending.Query,
		Repo:       filepath.Base(repoRoot),
		Candidates: pending.Candidates,
		Label:      chosenIdx,
	}
	appendTrainingLabel(edrDir, label)
	return true
}

// ClearPendingShortlist discards any pending shortlist (e.g., on non-focus commands).
func ClearPendingShortlist() {
	pendingMu.Lock()
	pendingShortlist = nil
	pendingMu.Unlock()
}

// TrainingLabelsPath returns the path to the training labels file.
func TrainingLabelsPath(edrDir string) string {
	return filepath.Join(edrDir, "training_labels.jsonl")
}

func appendTrainingLabel(edrDir string, label TrainingLabel) {
	if edrDir == "" {
		return
	}
	path := TrainingLabelsPath(edrDir)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	data, err := json.Marshal(label)
	if err != nil {
		return
	}
	f.Write(data)
	f.Write([]byte("\n"))
}

// CountTrainingLabels returns the number of labels in the training file.
func CountTrainingLabels(edrDir string) int {
	data, err := os.ReadFile(TrainingLabelsPath(edrDir))
	if err != nil {
		return 0
	}
	count := 0
	for _, b := range data {
		if b == '\n' {
			count++
		}
	}
	return count
}
