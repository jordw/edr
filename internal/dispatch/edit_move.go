package dispatch

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/staleness"
)

// validateMoveSpan returns an error if the symbol's byte range is unusable
// for a move/extract (zero or inverted). Surfacing a clean error here keeps
// downstream slicing from panicking with "slice bounds out of range" when an
// upstream resolver picks an overload-only declaration that the parser
// couldn't fully bracket.
func validateMoveSpan(s *index.SymbolInfo, role string) error {
	if s == nil {
		return fmt.Errorf("%s symbol: nil", role)
	}
	if s.EndByte <= s.StartByte {
		return fmt.Errorf("%s symbol %q in %s has no body span (likely an overload or forward declaration); pick the implementation",
			role, s.Name, output.Rel(s.File))
	}
	return nil
}

// smartEditMoveAfter moves a source symbol after a target symbol within the same file.
func smartEditMoveAfter(ctx context.Context, db index.SymbolStore, root string, args []string, targetName string, dryRun bool) (any, error) {
	// Resolve source symbol
	srcSym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, fmt.Errorf("source symbol: %w", err)
	}

	// Resolve target symbol — check if target specifies a different file (cross-file move).
	var tgtSym *index.SymbolInfo
	if parts := splitFileSymbol(targetName); parts != nil {
		tgtFile, err := db.ResolvePath(parts[0])
		if err != nil {
			return nil, fmt.Errorf("target file: %w", err)
		}
		tgtSym, err = db.GetSymbol(ctx, tgtFile, parts[1])
		if err != nil {
			return nil, fmt.Errorf("target symbol: %w", err)
		}
	} else {
		tgtSym, err = db.GetSymbol(ctx, srcSym.File, targetName)
		if err != nil {
			return nil, fmt.Errorf("target symbol: %w", err)
		}
	}

	if err := validateMoveSpan(srcSym, "source"); err != nil {
		return nil, err
	}
	if err := validateMoveSpan(tgtSym, "target"); err != nil {
		return nil, err
	}

	if srcSym.File != tgtSym.File {
		return smartEditMoveAcrossFiles(ctx, db, srcSym, tgtSym, dryRun)
	}

	data, err := os.ReadFile(srcSym.File)
	if err != nil {
		return nil, err
	}

	srcStart := int(srcSym.StartByte)
	srcEnd := int(srcSym.EndByte)
	// Include trailing newline in the cut
	if srcEnd < len(data) && data[srcEnd] == '\n' {
		srcEnd++
	}
	srcBody := string(data[srcStart:srcEnd])

	tgtEnd := int(tgtSym.EndByte)
	// Insert after target's trailing newline if present
	if tgtEnd < len(data) && data[tgtEnd] == '\n' {
		tgtEnd++
	}

	// Build the new file content: remove source, insert after target
	var result strings.Builder
	if srcStart < tgtEnd {
		// Source is before target: remove src, then insert after target
		result.Write(data[:srcStart])
		result.Write(data[srcEnd:tgtEnd])
		result.WriteString("\n")
		result.WriteString(srcBody)
		result.Write(data[tgtEnd:])
	} else {
		// Source is after target: insert after target, then remove src
		result.Write(data[:tgtEnd])
		result.WriteString("\n")
		result.WriteString(srcBody)
		result.Write(data[tgtEnd:srcStart])
		result.Write(data[srcEnd:])
	}

	newContent := result.String()
	diff := edit.UnifiedDiff(output.Rel(srcSym.File), data, []byte(newContent))

	if dryRun {
		return map[string]any{
			"file":   output.Rel(srcSym.File),
			"status": "dry_run",
			"diff":   diff,
			"symbol": srcSym.Name,
			"after":  tgtSym.Name,
		}, nil
	}

	// Write the new content
	hash, _ := edit.FileHash(srcSym.File)
	if err := os.WriteFile(srcSym.File, []byte(newContent), 0644); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}
	staleness.OpenTracker(db.EdrDir(), idx.DirtyTrackerName).Mark(output.Rel(srcSym.File))
	newHash, _ := edit.FileHash(srcSym.File)

	return map[string]any{
		"file":     output.Rel(srcSym.File),
		"status":   "applied",
		"diff":     diff,
		"hash":     newHash,
		"old_hash": hash,
		"symbol":   srcSym.Name,
		"after":    tgtSym.Name,
	}, nil
}

// smartEditMoveAcrossFiles moves a symbol from one file to another, placing it
// after the target symbol. Uses Transaction for atomic two-file writes.
func smartEditMoveAcrossFiles(_ context.Context, db index.SymbolStore, srcSym, tgtSym *index.SymbolInfo, dryRun bool) (any, error) {
	srcData, err := os.ReadFile(srcSym.File)
	if err != nil {
		return nil, fmt.Errorf("move: read source: %w", err)
	}
	dstData, err := os.ReadFile(tgtSym.File)
	if err != nil {
		return nil, fmt.Errorf("move: read dest: %w", err)
	}

	// Expand to include any doc comment preceding the symbol.
	docStart := int(expandToDocComment(srcSym.File, srcSym.StartByte))
	srcStart := docStart
	srcEnd := int(srcSym.EndByte)
	if srcEnd < len(srcData) && srcData[srcEnd] == '\n' {
		srcEnd++
	}
	// Also consume one leading blank line to avoid double-spacing.
	if srcStart > 0 && srcData[srcStart-1] == '\n' {
		srcStart--
	}
	srcBody := string(srcData[docStart:int(srcSym.EndByte)])

	// Remove from source.
	newSrc := make([]byte, 0, len(srcData)-(srcEnd-srcStart))
	newSrc = append(newSrc, srcData[:srcStart]...)
	newSrc = append(newSrc, srcData[srcEnd:]...)

	// Collapse runs of 3+ consecutive newlines down to 2 at the splice point.
	// This prevents artifacts like *//** when a doc comment block ended just
	// before the removed symbol and another begins right after.
	if srcStart > 0 && srcStart < len(newSrc) {
		i := srcStart
		// Count consecutive newlines around the splice.
		start := i
		for start > 0 && newSrc[start-1] == '\n' {
			start--
		}
		end := i
		for end < len(newSrc) && newSrc[end] == '\n' {
			end++
		}
		nlCount := end - start
		if nlCount > 2 {
			// Keep exactly 2 newlines (one blank line).
			cut := nlCount - 2
			copy(newSrc[start+2:], newSrc[end:])
			newSrc = newSrc[:len(newSrc)-cut]
		}
	}

	// Insert into dest after target symbol.
	tgtEnd := int(tgtSym.EndByte)
	if tgtEnd < len(dstData) && dstData[tgtEnd] == '\n' {
		tgtEnd++
	}
	insertion := "\n" + srcBody + "\n"
	newDst := make([]byte, 0, len(dstData)+len(insertion))
	newDst = append(newDst, dstData[:tgtEnd]...)
	newDst = append(newDst, []byte(insertion)...)
	newDst = append(newDst, dstData[tgtEnd:]...)

	srcDiff := edit.UnifiedDiff(output.Rel(srcSym.File), srcData, newSrc)
	dstDiff := edit.UnifiedDiff(output.Rel(tgtSym.File), dstData, newDst)
	combinedDiff := srcDiff + dstDiff

	if dryRun {
		return map[string]any{
			"file":   output.Rel(srcSym.File),
			"status": "dry_run",
			"diff":   combinedDiff,
			"symbol": srcSym.Name,
			"after":  tgtSym.Name,
			"dest":   output.Rel(tgtSym.File),
		}, nil
	}

	// Atomic commit via Transaction.
	srcHash := edit.HashBytes(srcData)
	dstHash := edit.HashBytes(dstData)
	tx := edit.NewTransaction()
	tx.Add(srcSym.File, 0, uint32(len(srcData)), string(newSrc), srcHash)
	tx.Add(tgtSym.File, 0, uint32(len(dstData)), string(newDst), dstHash)
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("move: %w", err)
	}

	tr := staleness.OpenTracker(db.EdrDir(), idx.DirtyTrackerName)
	tr.Mark(output.Rel(srcSym.File))
	tr.Mark(output.Rel(tgtSym.File))
	newHash, _ := edit.FileHash(tgtSym.File)

	return map[string]any{
		"file":   output.Rel(srcSym.File),
		"status": "applied",
		"diff":   combinedDiff,
		"hash":   newHash,
		"symbol": srcSym.Name,
		"after":  tgtSym.Name,
		"dest":   output.Rel(tgtSym.File),
	}, nil
}
