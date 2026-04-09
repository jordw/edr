package ranking

import (
	"encoding/json"
	"path/filepath"
)

// ExportExample represents one labeled/unlabeled training example.
type ExportExample struct {
	Query      string            `json:"query"`
	Repo       string            `json:"repo,omitempty"`
	Candidates []ExportCandidate `json:"candidates"`
	Label      int               `json:"label,omitempty"` // 0-based index of correct candidate
}

// ExportCandidate is the candidate format used in training data.
type ExportCandidate struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	File      string `json:"file"`
	StartLine uint32 `json:"start_line"`
	EndLine   uint32 `json:"end_line"`
}

// ExportCandidateList builds a training example from a query and candidates.
// The root is used to derive the repo name and make paths relative.
func ExportCandidateList(query, root string, candidates []CandidateFeatures) ExportExample {
	repo := filepath.Base(root)
	exported := make([]ExportCandidate, len(candidates))
	for i, c := range candidates {
		exported[i] = ExportCandidate{
			Name:      c.Name,
			Type:      c.Type,
			File:      c.File,
			StartLine: c.StartLine,
			EndLine:   c.EndLine,
		}
	}
	return ExportExample{
		Query:      query,
		Repo:       repo,
		Candidates: exported,
	}
}

// ExportJSON serializes a training example to JSON.
func ExportJSON(ex ExportExample) ([]byte, error) {
	return json.Marshal(ex)
}
