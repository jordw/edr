package cmd

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/session"
)

func renameVerifyFiles(result any, args []string) []string {
	if r, ok := result.(*output.RenameResult); ok && len(r.FilesChanged) > 0 {
		return r.FilesChanged
	}
	if len(args) == 0 {
		return nil
	}
	target := args[0]
	if idx := strings.Index(target, ":"); idx > 0 {
		target = target[:idx]
	}
	return []string{target}
}

func runPostMutationVerify(ctx context.Context, db index.SymbolStore, sess *session.Session, env *output.Envelope, edrDir, root string, files []string, revertedMsg string, verifyCommand, verifyLevel string) {
	verifyFlags := map[string]any{}
	if len(files) > 0 {
		verifyFlags["files"] = files
	}
	if verifyCommand != "" {
		verifyFlags["command"] = verifyCommand
	}
	if verifyLevel != "" {
		verifyFlags["level"] = verifyLevel
	}
	verifyResult, verifyErr := dispatch.Dispatch(ctx, db, "verify", []string{}, verifyFlags)
	if verifyErr != nil {
		env.SetVerify(map[string]any{"status": "failed", "error": verifyErr.Error()})
	} else {
		env.SetVerify(verifyResult)
	}

	verifyStatus := ""
	if vm, ok := env.Verify.(map[string]any); ok {
		verifyStatus, _ = vm["status"].(string)
	}
	if verifyStatus != "failed" || sess == nil {
		return
	}

	sessDir := filepath.Join(edrDir, "sessions")
	cpID := session.LatestAutoCheckpoint(sessDir)
	if cpID == "" {
		return
	}
	dirtyFiles := sess.GetDirtyFiles()
	if _, _, _, restoreErr := sess.RestoreCheckpoint(sessDir, root, cpID, false, dirtyFiles); restoreErr != nil {
		return
	}
	session.DropCheckpoint(sessDir, cpID)
	if vm, ok := env.Verify.(map[string]any); ok {
		vm["auto_undone"] = true
	}
	if len(env.Ops) > 0 {
		lastOp := env.Ops[len(env.Ops)-1]
		lastOp["status"] = "reverted"
		lastOp["msg"] = revertedMsg
		delete(lastOp, "read_back")
	}
}
