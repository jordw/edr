package dispatch

import (
	"context"
	"fmt"

	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
)

// runIndex handles "edr index" and "edr index --status".
func runIndex(_ context.Context, db index.SymbolStore, root string, _ []string, flags map[string]any) (any, error) {
	edrDir := db.EdrDir()

	if flagBool(flags, "status", false) {
		total := 0
		index.WalkRepoFiles(root, func(_ string) error {
			total++
			return nil
		})

		s := idx.GetStatus(root, edrDir)
		result := map[string]any{
			"status": "ok",
			"mode":   "status",
		}
		if s.Exists {
			result["files_indexed"] = s.Files
			result["files_total"] = total
			result["trigrams"] = s.Trigrams
			result["size_bytes"] = s.SizeBytes
			result["stale"] = s.Stale
			if total > 0 {
				result["coverage"] = fmt.Sprintf("%.0f%%", float64(s.Files)/float64(total)*100)
			}
		} else {
			result["files_indexed"] = 0
			result["files_total"] = total
			result["coverage"] = "0%"
		}
		return result, nil
	}

	// Full build
	err := idx.BuildFullFromWalk(root, edrDir, index.WalkRepoFiles, nil)
	if err != nil {
		return nil, err
	}

	s := idx.GetStatus(root, edrDir)
	return map[string]any{
		"status":        "built",
		"files_indexed": s.Files,
		"trigrams":      s.Trigrams,
		"size_bytes":    s.SizeBytes,
	}, nil
}
