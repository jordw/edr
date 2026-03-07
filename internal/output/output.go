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
	Body      string   `json:"body"`
	Callers   []Symbol `json:"callers,omitempty"`
	Deps      []Symbol `json:"deps,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

// EditResult reports the outcome of an edit operation.
type EditResult struct {
	OK         bool   `json:"ok"`
	File       string `json:"file"`
	Message    string `json:"message"`
	Hash       string `json:"hash,omitempty"`
	IndexError string `json:"index_error,omitempty"`
}

// RenameResult reports the outcome of a cross-file rename.
type RenameResult struct {
	OldName      string             `json:"old_name"`
	NewName      string             `json:"new_name"`
	FilesChanged []string           `json:"files_changed"`
	Occurrences  int                `json:"occurrences"`
	Hashes       map[string]string  `json:"hashes,omitempty"` // file -> new hash for chaining
	DryRun       bool               `json:"dry_run,omitempty"`
	Preview      []RenameOccurrence `json:"preview,omitempty"`
}

// RenameOccurrence describes a single reference that would be renamed.
type RenameOccurrence struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// GatherResult aggregates context around a target symbol.
type GatherResult struct {
	Target         Symbol            `json:"target"`
	TargetBody     string            `json:"target_body,omitempty"`
	Deps           []Symbol          `json:"deps,omitempty"`
	Callers        []Symbol          `json:"callers,omitempty"`
	CallerSnips    map[string]string `json:"caller_snippets,omitempty"`
	Tests          []Symbol          `json:"tests,omitempty"`
	TestSnips      map[string]string `json:"test_snippets,omitempty"`
	TotalTokens    int               `json:"total_tokens"`
	OmittedCallers int               `json:"omitted_callers,omitempty"`
	OmittedTests   int               `json:"omitted_tests,omitempty"`
	Truncated      bool              `json:"truncated,omitempty"`
}

// Print marshals v to indented JSON and writes it to stdout.
func Print(v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "output: marshal error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}

// PrintList prints a structured list of matches under the given label.
func PrintList(label string, items []Match) {
	wrapper := struct {
		Label   string  `json:"label"`
		Count   int     `json:"count"`
		Matches []Match `json:"matches"`
	}{
		Label:   label,
		Count:   len(items),
		Matches: items,
	}
	Print(wrapper)
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
