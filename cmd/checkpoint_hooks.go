package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/session"
)

// injectSessionHash adds stale-read protection for mutations. If the session
// has a prior read hash for the target file and no explicit --expect-hash was
// provided, inject the session hash so the edit layer validates it.
func injectSessionHash(sess *session.Session, cmdName string, args []string, flags map[string]any) {
	if sess == nil {
		return
	}
	if cmdName != "edit" && cmdName != "write" {
		return
	}
	if _, has := flags["expect_hash"]; has {
		return
	}
	if len(args) == 0 {
		return
	}
	target := args[0]
	if idx := strings.Index(target, ":"); idx > 0 {
		target = target[:idx]
	}
	if h := sess.CheckFileHash(target); h != "" {
		flags["expect_hash"] = h
	}
}

// patchCheckpointWithOldContents adds pre-mutation file snapshots to the most
// recent auto-checkpoint. This is needed for multi-file commands (rename,
// changesig) where secondary files aren't known until after dispatch. Without
// this patch, undo cannot restore secondary files because they weren't in the
// original checkpoint.
func patchCheckpointWithOldContents(edrDir, root string, oldContents map[string][]byte) {
	sessDir := filepath.Join(edrDir, "sessions")
	cpID := session.LatestAutoCheckpoint(sessDir)
	if cpID == "" {
		return
	}
	if err := session.PatchCheckpointFiles(sessDir, cpID, root, oldContents); err != nil {
		fmt.Fprintf(os.Stderr, "edr: patch checkpoint: %v\n", err)
	}
}
