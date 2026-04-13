package output

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

var root string

// SetRoot sets the repo root for relative path output.
func SetRoot(r string) {
	root = r
}

// Rel converts an absolute path to a repo-relative path.
func Rel(abs string) string {
	if root != "" && strings.HasPrefix(abs, root+"/") {
		return abs[len(root)+1:]
	}
	return abs
}

// Symbol describes a code symbol (function, type, variable, etc.).
type Symbol struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	File      string `json:"file,omitempty"`
	Lines     [2]int `json:"lines"`
	Summary   string `json:"summary,omitempty"`
	Qualifier string `json:"qualifier,omitempty"`
	Size      int    `json:"size"`
	Hash      string `json:"hash,omitempty"`
	Signature string `json:"signature,omitempty"`
}

// Match pairs a Symbol with a relevance score.
type Match struct {
	Symbol  Symbol  `json:"symbol"`
	Score   float64 `json:"score"`
	Body    string  `json:"body,omitempty"`
	Snippet string  `json:"snippet,omitempty"`
	Column  int     `json:"column,omitempty"`
}

// ExpandResult contains the full details of an expanded symbol.
type ExpandResult struct {
	Symbol    Symbol   `json:"symbol"`
	Body      string   `json:"content"`
	Callers   []Symbol `json:"callers,omitempty"`
	Deps      []Symbol `json:"deps,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

// EditResult reports the outcome of an edit operation.
type EditResult struct {
	File       string `json:"file"`
	Message    string `json:"message"`
	Hash       string `json:"hash,omitempty"`
	Status     string `json:"status,omitempty"`
	IndexError string `json:"index_error,omitempty"`
}

// RenameResult reports the outcome of a cross-file rename.
type RenameResult struct {
	OldName      string             `json:"old_name"`
	NewName      string             `json:"new_name"`
	FilesChanged []string           `json:"files_changed"`
	Occurrences  int                `json:"occurrences"`
	Status       string             `json:"status"` // "applied", "dry_run", "noop"
	Noop         bool               `json:"noop,omitempty"` // deprecated: use Status
	Hashes       map[string]string  `json:"hashes,omitempty"`
	DryRun       bool               `json:"dry_run,omitempty"` // deprecated: use Status
	Preview      []RenameOccurrence `json:"preview,omitempty"`
	Diffs        []RenameDiff       `json:"diffs,omitempty"`
	Warnings     []string           `json:"warnings,omitempty"`
	Truncated    bool               `json:"truncated,omitempty"`
	// OldContents holds pre-mutation file contents (relative path → content).
	// Used by checkpoint logic to snapshot secondary files for undo.
	// Excluded from JSON output.
	OldContents  map[string][]byte  `json:"-"`
}

// RenameDiff holds a per-file unified diff for dry-run rename preview.
type RenameDiff struct {
	File string `json:"file"`
	Diff string `json:"diff"`
}

// RenameOccurrence describes a single reference that would be renamed.
type RenameOccurrence struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Print marshals v to indented JSON and writes it to stdout.
// Falls back to a plain-text error message if marshaling fails.
func Print(v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stdout, "{\"error\":\"marshal error: %v\"}\n", err)
		return
	}
	fmt.Println(string(data))
}

// TokenEstimate returns an approximate token count for the given code string,
// using a heuristic of ~4 characters per token.
func TokenEstimate(code string) int {
	n := len(code)
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}
