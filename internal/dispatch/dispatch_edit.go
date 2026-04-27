package dispatch

import (
	"context"
	"strings"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/staleness"
)

func runSmartEdit(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	dryRun := flagBool(flags, "dry-run", false)
	readBack := flagBool(flags, "read_back", true)

	// Delegate to inner logic, then optionally attach read-back context.
	result, err := runSmartEditInner(ctx, db, root, args, flags, dryRun)
	if err != nil && strings.Contains(err.Error(), "hash mismatch") && flagBool(flags, "refresh_hash", false) {
		if refreshed := refreshEditHash(db, args, flags); refreshed {
			result, err = runSmartEditInner(ctx, db, root, args, flags, dryRun)
		}
	}
	if err != nil || !readBack {
		return result, err
	}

	// Annotate with target resolution provenance
	if m, ok := result.(map[string]any); ok {
		annotateEditMode(m, flags)
		// For dry-run, add index freshness context
		if dryRun {
			edrDir := db.EdrDir()
			if staleness.OpenTracker(edrDir, idx.DirtyTrackerName).IsDirty() {
				m["index_state"] = "dirty"
			} else if idx.HasSymbolIndex(edrDir) {
				m["index_state"] = "fresh"
			} else {
				m["index_state"] = "none"
			}
		}
	}

	return attachReadBack(ctx, db, result)
}

// annotateEditMode adds target_origin and edit_mode to the result for provenance.
func annotateEditMode(result map[string]any, flags map[string]any) {
	if flagString(flags, "where", "") != "" {
		result["target_origin"] = "where"
	} else if flagString(flags, "move_after", "") != "" {
		result["target_origin"] = "move_after"
	}

	if flagString(flags, "in", "") != "" {
		result["edit_mode"] = "scoped_text_match"
	} else if flagInt(flags, "insert_at", 0) > 0 {
		result["edit_mode"] = "insert_at"
	} else if flagInt(flags, "start_line", 0) > 0 {
		result["edit_mode"] = "line_range"
	} else if flagString(flags, "old_text", "") != "" {
		result["edit_mode"] = "text_match"
	} else if _, hasSym := result["symbol"]; hasSym {
		result["edit_mode"] = "symbol"
	}
}

func refreshEditHash(db index.SymbolStore, args []string, flags map[string]any) bool {
	if len(args) == 0 {
		return false
	}
	target := args[0]
	if parts := splitFileSymbol(target); parts != nil {
		target = parts[0]
	}
	file, err := db.ResolvePath(target)
	if err != nil {
		return false
	}
	currentHash, err := edit.FileHash(file)
	if err != nil || currentHash == "" {
		return false
	}
	flags["expect_hash"] = currentHash
	return true
}
