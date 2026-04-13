package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

func runRename(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	newName := flagString(flags, "new_name", "")
	if newName == "" {
		return nil, fmt.Errorf("rename: --to <new_name> is required")
	}
	dryRun := flagBool(flags, "dry_run", false)

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

	// Build a word-boundary regex for the old name.
	pattern := `\b` + regexp.QuoteMeta(oldName) + `\b`
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("rename: invalid symbol name for regex: %w", err)
	}

	// Find all symbols that reference the target.
	refs, err := db.FindSemanticReferences(ctx, oldName, sym.File)
	if err != nil {
		return nil, fmt.Errorf("rename: finding references: %w", err)
	}

	// Collect the set of files to scan. Always include the definition file.
	fileSet := map[string]bool{sym.File: true}
	for _, ref := range refs {
		fileSet[ref.File] = true
	}

	// Sort files for deterministic output.
	files := make([]string, 0, len(fileSet))
	for f := range fileSet {
		files = append(files, f)
	}
	sort.Strings(files)

	// For each file, find and replace all word-boundary matches.
	type fileEdit struct {
		file    string
		oldData []byte
		newData []byte
		count   int
	}
	var edits []fileEdit
	totalOccurrences := 0

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("rename: read %s: %w", output.Rel(file), err)
		}

		if !bytes.Contains(data, []byte(oldName)) {
			continue
		}

		newBytes := re.ReplaceAll(data, []byte(newName))
		if bytes.Equal(data, newBytes) {
			continue
		}

		count := countReplacements(data, newBytes, re, oldName, newName)
		edits = append(edits, fileEdit{
			file:    file,
			oldData: data,
			newData: newBytes,
			count:   count,
		})
		totalOccurrences += count
	}

	if len(edits) == 0 {
		return &output.RenameResult{
			OldName: oldName,
			NewName: newName,
			Status:  "noop",
		}, nil
	}

	// Build result.
	result := &output.RenameResult{
		OldName:     oldName,
		NewName:     newName,
		Occurrences: totalOccurrences,
	}

	for _, fe := range edits {
		diff := edit.UnifiedDiff(output.Rel(fe.file), fe.oldData, fe.newData)
		result.FilesChanged = append(result.FilesChanged, output.Rel(fe.file))
		result.Diffs = append(result.Diffs, output.RenameDiff{
			File: output.Rel(fe.file),
			Diff: diff,
		})
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
	for _, fe := range edits {
		idx.MarkDirty(db.EdrDir(), output.Rel(fe.file))
	}

	result.Status = "applied"
	return result, nil
}

// countReplacements counts how many word-boundary matches of oldName were
// replaced by comparing the regex match count on original data.
func countReplacements(oldData, _ []byte, re *regexp.Regexp, _, _ string) int {
	return len(re.FindAll(oldData, -1))
}
