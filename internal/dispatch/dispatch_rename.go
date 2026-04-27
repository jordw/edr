package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
	strict := flagBool(flags, "strict", false)
	if strict && force {
		return nil, fmt.Errorf("rename: --strict and --force are mutually exclusive")
	}
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
	var scopeWarnings []string
	if crossFile {
		if spans, warns, ok := scopeAwareCrossFileSpans(ctx, db, sym); ok {
			fileSpans = spans
			scopeWarnings = warns
			scopeDone = true
		}
	} else {
		if spans, ok := scopeAwareSameFileSpans(sym); ok {
			fileSpans[sym.File] = spans
			scopeDone = true
		}
	}

	// Strict mode: refuse before the legacy regex fallback runs, so
	// strict never silently degrades to name-match. Done before the
	// fileSpans build below so the refusal carries no edits.
	if strict && !scopeDone {
		return &output.RenameResult{
			OldName:        oldName,
			NewName:        newName,
			Status:         "refused",
			RefusedReason:  "strict_refused",
			RefusedDetail:  fmt.Sprintf("scope-aware rename is not available for %s; --strict refuses to fall back to name-match", filepath.Ext(sym.File)),
			SeeAlso:        fmt.Sprintf("edr refs-to %s:%s --include-name-match", output.Rel(sym.File), oldName),
		}, nil
	}

	// Strict binding-tier audit: even when scope ran, the rewrite set
	// may include probable/ambiguous refs that strict refuses on.
	if strict && scopeDone {
		audit, ok := auditSameFileBinding(sym)
		if !ok {
			return &output.RenameResult{
				OldName:        oldName,
				NewName:        newName,
				Status:         "refused",
				RefusedReason:  "strict_refused",
				RefusedDetail:  "could not audit binding tiers for the target file",
				SeeAlso:        fmt.Sprintf("edr refs-to %s:%s --include-name-match", output.Rel(sym.File), oldName),
			}, nil
		}
		if audit.nonResolvedTotal() > 0 {
			refusedExamples := make([]output.RefusedExample, 0, len(audit.Refused))
			for _, r := range audit.Refused {
				refusedExamples = append(refusedExamples, output.RefusedExample{
					File:   r.File,
					Line:   r.Line,
					Tier:   r.Tier,
					Reason: r.Reason,
				})
			}
			counts := make(map[string]int, len(audit.Counts))
			for k, v := range audit.Counts {
				counts[k] = v
			}
			return &output.RenameResult{
				OldName:         oldName,
				NewName:         newName,
				Status:          "refused",
				RefusedReason:   "strict_refused",
				RefusedCounts:   counts,
				RefusedExamples: refusedExamples,
				SeeAlso:         fmt.Sprintf("edr refs-to %s:%s --include-name-match", output.Rel(sym.File), oldName),
			}, nil
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
	result.Warnings = append(result.Warnings, scopeWarnings...)
	if mode == "name-match" {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"rename used name-based matching: scope builder for %s is not yet admitted for writes; verify diffs for cross-class false positives before committing",
			filepath.Ext(sym.File)))
	}
	if idx.IsStale(root, db.EdrDir()) {
		result.Warnings = append(result.Warnings,
			"index is stale — cross-file refs may be undercounted; run 'edr index' to refresh, then re-run rename")
	}

	// Companion-file mentions: scan template / non-parsed files (ERB, HAML,
	// Slim, Blade, etc.) tied to the source language for word-bounded
	// occurrences of oldName. Rename does not rewrite these — receiver type
	// can't be inferred without runtime info — but a silent miss here is
	// the false-success failure mode (rename "succeeds" while view callers
	// still reference the old name). Emit a warning so the user can audit.
	if companionTotal, companionMentions := companionFileMentions(root, db.EdrDir(), sym.File, oldName, fileSpans); companionTotal > 0 {
		sort.Slice(companionMentions, func(i, j int) bool {
			return companionMentions[i].count > companionMentions[j].count
		})
		head := companionMentions
		const sampleCap = 5
		if len(head) > sampleCap {
			head = head[:sampleCap]
		}
		var sample []string
		for _, m := range head {
			sample = append(sample, fmt.Sprintf("%s (%d)", m.file, m.count))
		}
		extra := ""
		if len(companionMentions) > len(head) {
			extra = fmt.Sprintf(" +%d more", len(companionMentions)-len(head))
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"%d mention(s) of %q in %d template/companion file(s) not rewritten by rename — review manually: %s%s",
			companionTotal, oldName, len(companionMentions), strings.Join(sample, ", "), extra))
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
