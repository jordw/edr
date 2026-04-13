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

	// Build a map of file → symbol byte ranges to replace within.
	// The definition symbol is always included, with its span extended
	// backwards to cover preceding doc comments that may contain the name.
	type span struct{ start, end uint32 }
	fileSpans := map[string][]span{}
	defStart := expandToDocComment(sym.File, sym.StartByte)
	fileSpans[sym.File] = append(fileSpans[sym.File], span{defStart, sym.EndByte})
	for _, ref := range refs {
		fileSpans[ref.File] = append(fileSpans[ref.File], span{ref.StartByte, ref.EndByte})
	}

	// Sort files for deterministic output.
	files := make([]string, 0, len(fileSpans))
	for f := range fileSpans {
		files = append(files, f)
	}
	sort.Strings(files)

	// For each file, replace only within the identified symbol spans.
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

		spans := fileSpans[file]
		// Sort spans by start offset for a single forward pass.
		sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

		// Build new file content: copy unchanged regions verbatim,
		// apply regex replacement only within span ranges.
		var buf bytes.Buffer
		pos := 0
		count := 0
		for _, s := range spans {
			start := int(s.start)
			end := int(s.end)
			if start > len(data) {
				start = len(data)
			}
			if end > len(data) {
				end = len(data)
			}
			if start < pos {
				// Overlapping or out-of-order span; skip.
				continue
			}
			// Copy unchanged region before this span.
			buf.Write(data[pos:start])
			// Replace within the span.
			region := data[start:end]
			replaced := re.ReplaceAll(region, []byte(newName))
			count += len(re.FindAll(region, -1))
			buf.Write(replaced)
			pos = end
		}
		// Copy remainder after last span.
		buf.Write(data[pos:])

		newData := buf.Bytes()
		if bytes.Equal(data, newData) {
			continue
		}

		edits = append(edits, fileEdit{
			file:    file,
			oldData: data,
			newData: newData,
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
		} else {
			break
		}
	}
	return uint32(pos)
}
