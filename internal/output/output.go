package output

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// CLI-process-scoped repo root used by Rel as a fallback. Acceptable for
// the single-root CLI; programmatic / multi-root callers should use RelFor
// with an explicit root instead so two simultaneous Dispatch calls against
// different repos don't race on this global.
var root string

// SetRoot updates the process-level repo root used by Rel. Each Dispatch
// call sets it (no longer once-only) so a second Dispatch against a
// different repo within the same process gets the right root for diff
// labels and result paths.
func SetRoot(r string) {
	root = r
}

// Rel converts an absolute path to a repo-relative path using the global
// root set by SetRoot. Convenience wrapper for the CLI; multi-root callers
// should use RelFor.
func Rel(abs string) string {
	return RelFor(root, abs)
}

// RelFor converts an absolute path to a repo-relative path against an
// explicit root. Prefer this in code that may be called from contexts
// without a stable global (e.g., parallel dispatch over multiple roots).
func RelFor(root, abs string) string {
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
	// Mode reports how reference sites were located: "scope" means the
	// scope builder resolved each span with shadow filtering; "name-match"
	// means the language's scope builder is not yet admitted for writes
	// and the regex + symbol-index fallback was used. Consumers should
	// treat "name-match" results as possibly containing cross-class
	// false positives and verify diffs before committing.
	Mode         string             `json:"mode,omitempty"`
	FilesChanged []string           `json:"files_changed"`
	Occurrences  int                `json:"occurrences"`
	// CodeOccurrences and CommentOccurrences split Occurrences by where the
	// match landed. A C rename of `sched_tick` typically produces a few code
	// edits (decl, def, calls) plus a handful of comment mentions ("tick path:
	// sched_tick -> ..."). Surfacing them separately lets the caller decide
	// whether the comment edits are intended.
	CodeOccurrences    int            `json:"code_occurrences,omitempty"`
	CommentOccurrences int            `json:"comment_occurrences,omitempty"`
	// CodeMentions is the word-bounded text count of OldName in code regions
	// of the touched files. When CodeMentions > CodeOccurrences, some
	// mentions in code were not rewritten — check for missed refs (most
	// commonly a parser bug) before trusting the result. Shadowed locals and
	// string-literal lookalikes also bump this, so it is signal not proof.
	CodeMentions       int            `json:"code_mentions,omitempty"`
	CommentMode        string         `json:"comment_mode,omitempty"` // "rewrite" (default) or "skip"
	Status       string             `json:"status"` // "applied", "dry_run", "noop", "refused"
	Hashes       map[string]string  `json:"hashes,omitempty"`
	Preview      []RenameOccurrence `json:"preview,omitempty"`
	Diffs        []RenameDiff       `json:"diffs,omitempty"`
	Warnings     []string           `json:"warnings,omitempty"`
	Truncated    bool               `json:"truncated,omitempty"`

	// Refusal fields. Set when Status == "refused" — currently emitted
	// by --strict when scope path can't run or when the rewrite set
	// includes non-Resolved refs. RefusedCounts maps tier name (e.g.
	// "probable") to the exact count; RefusedExamples is a capped
	// sample of file:line entries for human review. SeeAlso names the
	// follow-up command an agent can run to inspect the broader set.
	RefusedReason   string           `json:"refused_reason,omitempty"`
	RefusedDetail   string           `json:"refused_detail,omitempty"`
	RefusedCounts   map[string]int   `json:"refused_counts,omitempty"`
	RefusedExamples []RefusedExample `json:"refused_examples,omitempty"`
	SeeAlso         string           `json:"see_also,omitempty"`

	// OldContents holds pre-mutation file contents (relative path → content).
	// Used by checkpoint logic to snapshot secondary files for undo.
	// Excluded from JSON output.
	OldContents  map[string][]byte  `json:"-"`
}

// RefusedExample is one entry in a refusal report — a single ref that
// blocked --strict from rewriting.
type RefusedExample struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Tier   string `json:"tier"`             // "ambiguous" | "probable" | "unresolved"
	Reason string `json:"reason,omitempty"` // Binding.Reason, if set
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
