package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/staleness"
)

// span identifies a byte range in a file that rename should rewrite.
// Bytes [start, end) are overwritten with the new name; dispatch
// handlers and the scope-aware resolver are responsible for emitting
// only identifier-tight ranges, with no leading separator (`.`, `->`,
// `::`) or trailing punctuation.
type span struct {
	start, end uint32
}

func runRename(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	newName := flagString(flags, "new_name", "")
	if newName == "" {
		return nil, fmt.Errorf("rename: --to <new_name> is required")
	}
	dryRun := flagBool(flags, "dry_run", false)
	crossFile := flagBool(flags, "cross_file", false)
	force := flagBool(flags, "force", false)
	commentMode := flagString(flags, "comments", "rewrite")
	switch commentMode {
	case "rewrite", "skip":
	default:
		return nil, fmt.Errorf("rename: --comments must be 'rewrite' or 'skip' (got %q)", commentMode)
	}
	updateComments := flagBool(flags, "update_comments", false)

	// Resolve the symbol to rename.
	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, fmt.Errorf("rename: %w", err)
	}
	oldName := sym.Name

	if oldName == newName {
		return &output.RenameResult{
			OldName: oldName,
			NewName: newName,
			Status:  "noop",
		}, nil
	}

	// File → byte ranges to replace. Each span covers exactly the
	// identifier bytes; the apply loop overwrites them with newName
	// and gates --comments=skip via positionInComment.
	fileSpans := map[string][]span{}

	// Scope-aware path: resolve rename targets via binding analysis so
	// shadowed locals with the same name are NOT renamed. Same-file
	// uses scopeAwareSameFileSpans; cross-file narrows candidates via
	// the symbol index's import-graph-aware FindSemanticReferences
	// and then scope-filters each file's refs (excluding refs that
	// bind to a local same-name decl, i.e. shadows). Falls back to
	// the regex+symbol-index path on any failure.
	scopeDone := false
	if crossFile {
		if spans, ok := scopeAwareCrossFileSpans(ctx, db, sym); ok {
			fileSpans = spans
			scopeDone = true
		}
	} else {
		if spans, ok := scopeAwareSameFileSpans(sym); ok {
			fileSpans[sym.File] = spans
			scopeDone = true
		}
	}

	// Legacy regex+symbol-index path: unsupported languages, or cases
	// scope could not resolve.
	if !scopeDone {
		var refs []index.SymbolInfo
		if crossFile {
			refs, err = db.FindSemanticReferences(ctx, oldName, sym.File)
		} else {
			refs, err = db.FindSameFileCallers(ctx, oldName, sym.File)
		}
		if err != nil {
			return nil, fmt.Errorf("rename: finding references: %w", err)
		}

		// The legacy path has no scope binding, so it scans the
		// symbol-index range — and each ref's range — for word-
		// bounded mentions of oldName. Some symbol-index langs
		// report ref ranges wider than the bare ident (e.g. a whole
		// call expression), so the textual scan narrows them down
		// before substitution.
		defStart := expandToDocComment(sym.File, sym.StartByte)
		if data, derr := os.ReadFile(sym.File); derr == nil {
			fileSpans[sym.File] = append(fileSpans[sym.File],
				findIdentOccurrences(data, defStart, sym.EndByte, oldName)...)
		}
		refData := map[string][]byte{}
		for _, ref := range refs {
			data, ok := refData[ref.File]
			if !ok {
				if d, derr := os.ReadFile(ref.File); derr == nil {
					data = d
					refData[ref.File] = d
				}
			}
			if data != nil {
				fileSpans[ref.File] = append(fileSpans[ref.File],
					findIdentOccurrences(data, ref.StartByte, ref.EndByte, oldName)...)
			}
		}
	}

	// Sort files for deterministic output.
	files := make([]string, 0, len(fileSpans))
	for f := range fileSpans {
		files = append(files, f)
	}
	sort.Strings(files)

	// For each file, replace only within the identified symbol spans.
	type fileEdit struct {
		file         string
		oldData      []byte
		newData      []byte
		count        int
		commentCount int
	}
	var edits []fileEdit
	totalCodeEdits := 0
	totalCommentMatches := 0
	totalCodeMentions := 0 // word-bounded oldName mentions in code regions of touched files; sanity check vs totalCodeEdits to flag misses (e.g. receiver-bug class)

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("rename: read %s: %w", output.Rel(file), err)
		}

		if !bytes.Contains(data, []byte(oldName)) {
			continue
		}

		fileExt := commentSyntaxFor(file)
		totalCodeMentions += countNameInCode(data, oldName, fileExt)

		// --update-comments: scan touched files for word-bounded oldName
		// mentions in comments and add them to the rewrite set. The resolver
		// only returns the declaration's leading doc comment via
		// expandToDocComment; arbitrary doc/inline mentions of the type
		// elsewhere are missed without this opt-in pass.
		if updateComments && commentMode != "skip" {
			fileSpans[file] = append(fileSpans[file], findCommentMentions(data, oldName, fileExt)...)
		}

		spans := dedupSpans(fileSpans[file])
		// Sort spans by start offset for a single forward pass.
		sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

		// Span-based substitution: each span identifies the exact
		// bytes to overwrite with newName. Comment classification is
		// per-span (positionInComment of start byte).
		var buf bytes.Buffer
		pos := 0
		codeCount := 0
		commentCount := 0
		for _, s := range spans {
			start := int(s.start)
			end := int(s.end)
			if start > len(data) {
				start = len(data)
			}
			if end > len(data) {
				end = len(data)
			}
			if start < pos || start > end {
				// Overlapping, out-of-order, or malformed (start>end);
				// skip so a bad upstream span can't crash the apply.
				continue
			}
			buf.Write(data[pos:start])

			inComment := positionInComment(data, start, fileExt)
			if inComment {
				commentCount++
				if commentMode == "skip" {
					buf.Write(data[start:end])
					pos = end
					continue
				}
			} else {
				codeCount++
			}
			buf.WriteString(newName)
			pos = end
		}
		// Copy remainder after last span.
		buf.Write(data[pos:])

		// Count matches even if we ended up with no edits (skip mode +
		// only-comment matches). The summary should still report what was
		// found vs ignored.
		totalCodeEdits += codeCount
		totalCommentMatches += commentCount

		newData := buf.Bytes()
		if bytes.Equal(data, newData) {
			continue
		}

		count := codeCount
		if commentMode == "rewrite" {
			count += commentCount
		}
		edits = append(edits, fileEdit{
			file:         file,
			oldData:      data,
			newData:      newData,
			count:        count,
			commentCount: commentCount,
		})
	}

	if len(edits) == 0 {
		return &output.RenameResult{
			OldName: oldName,
			NewName: newName,
			Status:  "noop",
		}, nil
	}

	// Blast-radius gate: cross-file rename touching large swaths of the repo
	// almost certainly indicates a common-name collision (e.g. renaming "init"
	// matches thousands of unrelated identifiers). Refuse unless --force.
	const (
		crossFileFileCap = 50
		crossFileEditCap = 200
	)
	totalOccurrences := totalCodeEdits
	if commentMode == "rewrite" {
		totalOccurrences += totalCommentMatches
	}
	if crossFile && !force && (len(edits) > crossFileFileCap || totalOccurrences > crossFileEditCap) {
		return nil, fmt.Errorf("rename refused: --cross-file would edit %d files and %d occurrences (limits: %d files, %d occurrences). The name %q likely collides with unrelated identifiers. Re-run with --force to proceed, --dry-run to inspect, or narrow scope",
			len(edits), totalOccurrences, crossFileFileCap, crossFileEditCap, oldName)
	}

	// Build result. Mode reports whether the scope-aware path carried
	// the rewrite ("scope") or the regex + symbol-index fallback did
	// ("name-match") — the latter has no shadow filtering and may touch
	// cross-class same-name identifiers, so we attach a warning.
	mode := "scope"
	if !scopeDone {
		mode = "name-match"
	}
	result := &output.RenameResult{
		OldName:            oldName,
		NewName:            newName,
		Mode:               mode,
		Occurrences:        totalOccurrences,
		CodeOccurrences:    totalCodeEdits,
		CommentOccurrences: totalCommentMatches,
		CodeMentions:       totalCodeMentions,
		CommentMode:        commentMode,
	}
	if totalCodeMentions > totalCodeEdits {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"%d code mention(s) of %q remain unrewritten — possible missed refs (or shadowed locals / string-literal lookalikes); inspect diff before committing",
			totalCodeMentions-totalCodeEdits, oldName))
	}
	if mode == "name-match" {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"rename used name-based matching: scope builder for %s is not yet admitted for writes; verify diffs for cross-class false positives before committing",
			filepath.Ext(sym.File)))
	}
	if idx.IsStale(root, db.EdrDir()) {
		result.Warnings = append(result.Warnings,
			"index is stale — cross-file refs may be undercounted; run 'edr index' to refresh, then re-run rename")
	}

	result.OldContents = make(map[string][]byte, len(edits))
	for _, fe := range edits {
		rel := output.Rel(fe.file)
		diff := edit.UnifiedDiff(rel, fe.oldData, fe.newData)
		result.FilesChanged = append(result.FilesChanged, rel)
		result.Diffs = append(result.Diffs, output.RenameDiff{
			File: rel,
			Diff: diff,
		})
		result.OldContents[rel] = fe.oldData
	}

	if dryRun {
		result.Status = "dry_run"
		return result, nil
	}

	// Apply all edits atomically via Transaction.
	tx := edit.NewTransaction()
	for _, fe := range edits {
		hash := edit.HashBytes(fe.oldData)
		// Replace entire file content.
		tx.Add(fe.file, 0, uint32(len(fe.oldData)), string(fe.newData), hash)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("rename: %w", err)
	}

	// Mark files dirty in the trigram index.
	tr := staleness.OpenTracker(db.EdrDir(), idx.DirtyTrackerName)
	for _, fe := range edits {
		tr.Mark(output.Rel(fe.file))
	}

	result.Status = "applied"
	return result, nil
}

// commentSyntaxFor returns a small bitmask describing which comment styles
// the file's language uses. Approximate but enough to cover the languages we
// support: line comments via // for C-family/JS/TS/Rust/Go, # for Python/Ruby
// /shell/Makefile, -- for SQL/Lua, ; for Lisp/asm. Block comments via /* */
// for C-family/JS/TS/Rust/Go, """ for Python.
func commentSyntaxFor(file string) commentSyntax {
	ext := ""
	for i := len(file) - 1; i >= 0; i-- {
		if file[i] == '.' {
			ext = file[i:]
			break
		}
		if file[i] == '/' {
			break
		}
	}
	switch ext {
	case ".c", ".h", ".cpp", ".cc", ".cxx", ".hpp", ".hxx", ".hh",
		".m", ".mm", ".java", ".kt", ".kts", ".scala", ".sc",
		".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts",
		".go", ".rs", ".swift", ".cs", ".dart", ".groovy", ".php":
		return commentSyntax{slashSlash: true, slashStar: true}
	case ".py", ".pyi":
		return commentSyntax{hash: true, tripleQuote: true}
	case ".rb", ".sh", ".bash", ".zsh", ".pl", ".pm", ".tcl", ".cmake":
		return commentSyntax{hash: true}
	case ".lua", ".sql":
		return commentSyntax{dashDash: true}
	}
	// Default: slash-slash + slash-star covers most curly-brace languages we
	// haven't enumerated.
	return commentSyntax{slashSlash: true, slashStar: true}
}

type commentSyntax struct {
	slashSlash  bool
	slashStar   bool
	hash        bool
	dashDash    bool
	tripleQuote bool
}

// positionInComment reports whether the byte at `pos` in `data` falls inside
// a comment, scanning back to the start of the line for line-comments and
// scanning the file for the most recent unterminated block-comment opener.
//
// Cheap and approximate — we don't tokenize strings, so a comment marker
// inside a string literal will be misclassified. For the rename use case the
// trade-off is fine: a false positive (treating "// foo" inside a string as
// a comment) just means we report it as a comment edit, which is at worst
// noisy in the summary, never an incorrect edit.
func positionInComment(data []byte, pos int, syn commentSyntax) bool {
	if pos < 0 || pos >= len(data) {
		return false
	}
	// Scan back to the line start.
	lineStart := pos
	for lineStart > 0 && data[lineStart-1] != '\n' {
		lineStart--
	}
	for i := lineStart; i < pos; i++ {
		c := data[i]
		if syn.slashSlash && c == '/' && i+1 < len(data) && data[i+1] == '/' {
			return true
		}
		if syn.hash && c == '#' {
			return true
		}
		if syn.dashDash && c == '-' && i+1 < len(data) && data[i+1] == '-' {
			return true
		}
	}
	// Block-comment scan: look for the nearest /* before pos that isn't
	// closed before pos. (Skip when the language doesn't use /* */.)
	if syn.slashStar {
		open := bytes.LastIndex(data[:pos], []byte("/*"))
		if open >= 0 {
			close := bytes.Index(data[open:pos], []byte("*/"))
			if close < 0 {
				return true
			}
		}
	}
	return false
}

// expandToDocComment scans backwards from startByte to include preceding
// comment lines (// or #) that are part of the symbol's documentation.
func expandToDocComment(file string, startByte uint32) uint32 {
	data, err := os.ReadFile(file)
	if err != nil || startByte == 0 {
		return startByte
	}

	pos := int(startByte)
	for pos > 0 {
		// Skip backwards over the newline before startByte.
		nl := pos - 1
		if nl < 0 || data[nl] != '\n' {
			break
		}
		// Find the start of the previous line.
		lineStart := nl
		for lineStart > 0 && data[lineStart-1] != '\n' {
			lineStart--
		}
		line := bytes.TrimSpace(data[lineStart:nl])
		if len(line) >= 2 && line[0] == '/' && line[1] == '/' {
			pos = lineStart
		} else if len(line) >= 1 && line[0] == '#' {
			pos = lineStart
		} else if len(line) >= 2 && line[0] == '/' && line[1] == '*' {
			pos = lineStart
		} else if len(line) >= 3 && bytes.HasPrefix(line, []byte("///")) {
			pos = lineStart
		} else if len(line) >= 1 && line[0] == '*' {
			// Middle or end of a block comment (/** ... */).
			pos = lineStart
		} else {
			break
		}
	}
	return uint32(pos)
}

// findIdentOccurrences scans data[lo:hi] for word-bounded occurrences of
// name and returns one span per match. Used to pick up oldName mentions in
// a symbol's leading doc-comment block once the apply layer is span-based
// (the legacy regex sweep handled this implicitly).
func findIdentOccurrences(data []byte, lo, hi uint32, name string) []span {
	if name == "" {
		return nil
	}
	if hi > uint32(len(data)) {
		hi = uint32(len(data))
	}
	if lo >= hi {
		return nil
	}
	var out []span
	nb := []byte(name)
	i := int(lo)
	end := int(hi)
	for i+len(nb) <= end {
		idx := bytes.Index(data[i:end], nb)
		if idx < 0 {
			break
		}
		abs := i + idx
		absEnd := abs + len(nb)
		leftOK := abs == 0 || !isIdentByte(data[abs-1])
		rightOK := absEnd >= len(data) || !isIdentByte(data[absEnd])
		if leftOK && rightOK {
			out = append(out, span{uint32(abs), uint32(absEnd)})
		}
		i = abs + 1
	}
	return out
}

// dedupSpans drops exact duplicates (same start+end) and contained
// duplicates so the apply pass doesn't double-count or skip emissions
// from multiple handlers (e.g. same-file decl + hierarchy emit).
func dedupSpans(in []span) []span {
	if len(in) <= 1 {
		return in
	}
	seen := make(map[uint64]bool, len(in))
	out := make([]span, 0, len(in))
	for _, s := range in {
		k := uint64(s.start)<<32 | uint64(s.end)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

func isIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_'
}

// findCommentMentions returns spans for word-bounded occurrences of name
// inside comment regions of data (per syn). Used by --update-comments to
// rewrite doc/comment mentions of the renamed symbol that the symbol-graph
// resolver does not return as refs (it only expands the *declaration's*
// leading doc comment, not arbitrary mentions in unrelated comments).
func findCommentMentions(data []byte, name string, syn commentSyntax) []span {
	if name == "" {
		return nil
	}
	nb := []byte(name)
	var out []span
	i := 0
	for i+len(nb) <= len(data) {
		idx := bytes.Index(data[i:], nb)
		if idx < 0 {
			break
		}
		abs := i + idx
		absEnd := abs + len(nb)
		leftOK := abs == 0 || !isIdentByte(data[abs-1])
		rightOK := absEnd >= len(data) || !isIdentByte(data[absEnd])
		if leftOK && rightOK && positionInComment(data, abs, syn) {
			out = append(out, span{uint32(abs), uint32(absEnd)})
		}
		i = abs + 1
	}
	return out
}

// countNameInCode counts word-bounded occurrences of name in data that are
// NOT inside a comment (per syn). Used as a sanity check against the resolver:
// if the resolver finds fewer code spans than this returns, some references
// were missed (or are intentionally skipped — shadowed locals, look-alikes
// in string literals — so this is signal not proof).
func countNameInCode(data []byte, name string, syn commentSyntax) int {
	if name == "" {
		return 0
	}
	nb := []byte(name)
	count := 0
	i := 0
	for i+len(nb) <= len(data) {
		idx := bytes.Index(data[i:], nb)
		if idx < 0 {
			break
		}
		abs := i + idx
		absEnd := abs + len(nb)
		leftOK := abs == 0 || !isIdentByte(data[abs-1])
		rightOK := absEnd >= len(data) || !isIdentByte(data[absEnd])
		if leftOK && rightOK && !positionInComment(data, abs, syn) {
			count++
		}
		i = abs + 1
	}
	return count
}
