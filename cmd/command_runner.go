package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/cmdspec"
	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/session"
	"github.com/spf13/cobra"
)

// dispatchCmd is the common pattern: open DB, dispatch, wrap in envelope, print.
// Loads a file-backed session when EDR_SESSION is set.
func dispatchCmd(cmd *cobra.Command, cmdName string, args []string) error {
	root := getRoot(cmd)
	flags := extractFlags(cmd)
	if cmdName == "focus" && cmd.Flags().Changed("expand") {
		args = normalizeExpandArgs(args, flags)
	}
	if err := resolveAtFiles(root, flags); err != nil {
		return err
	}

	db, err := openStore(root)
	if err != nil {
		return err
	}
	defer db.Close()

	edrDir := db.EdrDir()
	sess, saveSess := session.LoadSession(edrDir, db.Root())
	defer saveSess()

	injectSessionHash(sess, cmdName, args, flags)

	// Auto-checkpoint before mutations so undo can restore
	dryRun, _ := flags["dry_run"].(bool)
	if !dryRun && sess != nil && cmdspec.ModifiesState(cmdName) {
		dirtyFiles := sess.GetDirtyFiles()
		var target string
		if len(args) > 0 {
			target = args[0]
			if idx := strings.Index(target, ":"); idx > 0 {
				target = target[:idx]
			}
			found := false
			for _, f := range dirtyFiles {
				if f == target {
					found = true
					break
				}
			}
			if !found {
				dirtyFiles = append(dirtyFiles, target)
			}
		}
		label := cmdName
		if len(args) > 0 {
			label = cmdName + "_" + args[0]
		}
		sessDir := filepath.Join(edrDir, "sessions")
		if _, err := sess.CreateAutoCheckpoint(sessDir, root, label, dirtyFiles); err != nil {
			fmt.Fprintf(os.Stderr, "edr: checkpoint failed: %v\n", err)
		}
		// Stage pre-mutation content of this op's target into the active
		// transaction's checkpoint. First-write-wins — if the file was
		// already edited earlier in the txn, its pre-txn snapshot stays.
		if sess.ActiveTxn != "" && target != "" {
			_ = sess.AppendFilesToCheckpoint(sessDir, root, sess.ActiveTxn, []string{target})
		}
	}

	env := output.NewEnvelope(cmdName)
	opID := cmdName[:1] + "0"

	result, err := dispatch.Dispatch(context.Background(), db, cmdName, args, flags)
	if err != nil {
		addDispatchFailedOp(env, opID, cmdName, err, sess)
		env.ComputeOK()
		output.PrintEnvelope(env)
		return silentError{code: 1}
	}

	// Multi-file commands (rename) return OldContents so the checkpoint can
	// be patched to include secondary files. Without this, undo cannot
	// restore files it didn't know about at checkpoint-creation time.
	if r, ok := result.(*output.RenameResult); ok && len(r.OldContents) > 0 && sess != nil {
		patchCheckpointWithOldContents(edrDir, root, r.OldContents)
	}

	// Multi-result: expand into individual ops (e.g. multi-file read)
	if multi, ok := result.(dispatch.MultiResults); ok {
		for i, r := range multi {
			mOpID := fmt.Sprintf("%s%d", string(cmdName[0]), i)
			if !r.OK {
				env.AddFailedOp(mOpID, cmdName, r.Error)
				continue
			}
			// Apply session post-processing per result
			data, marshalErr := json.Marshal(r.Result)
			if marshalErr == nil {
				processed := sess.PostProcess(r.Cmd, args, map[string]any{}, r.Result, string(data))
				if processed != string(data) {
					var postResult any
					json.Unmarshal([]byte(processed), &postResult)
					r.Result = postResult
				}
			}
			env.AddOp(mOpID, cmdName, r.Result)
		}
	} else {
		// Extract and strip internal signature field before any serialization
		extractAndStripSignature(sess, cmdName, args, result)

		// Apply session post-processing (delta reads, body dedup)
		data, marshalErr := json.Marshal(result)
		if marshalErr == nil {
			processed := sess.PostProcess(cmdName, args, flags, result, string(data))
			if processed != string(data) {
				var postResult any
				json.Unmarshal([]byte(processed), &postResult)
				result = postResult
			}
		}

		env.AddOp(opID, cmdName, result)
	}

	if cmdName == "rename" {
		dryRun, _ := flags["dry_run"].(bool)
		doVerify, _ := flags["verify"].(bool)
		status, _ := resultStatus(result)
		if !dryRun && doVerify && status == "applied" {
			verifyFiles := renameVerifyFiles(result, args)
			verifyCmd, _ := flags["verify_command"].(string)
			verifyLvl, _ := flags["verify_level"].(string)
			runPostMutationVerify(context.Background(), db, sess, env, edrDir, root, verifyFiles, "rename applied then reverted: verify failed", verifyCmd, verifyLvl)
		} else if dryRun && doVerify {
			env.SetVerify(map[string]any{"status": "skipped", "reason": "dry run"})
		}
		if vm, ok := env.Verify.(map[string]any); ok {
			if vs, ok := vm["status"].(string); ok && vs != "skipped" {
				sess.RecordVerify(vs)
			}
		}
	}
	env.ComputeOK()

	// Record op in session log
	recordOp(sess, cmdName, args, flags, result, err == nil)

	output.PrintEnvelope(env)
	if !env.OK {
		return silentError{code: 1}
	}
	return nil
}

func normalizeExpandArgs(args []string, flags map[string]any) []string {
	if len(args) < 2 {
		return args
	}
	mode := args[len(args)-1]
	switch mode {
	case "deps", "callers", "both":
		flags["expand"] = mode
		return args[:len(args)-1]
	default:
		return args
	}
}

// dispatchCmdWithStdin is like dispatchCmd but reads stdin into a flag first.
func dispatchCmdWithStdin(cmd *cobra.Command, cmdName string, args []string, stdinKey string) error {
	root := getRoot(cmd)
	flags := extractFlags(cmd)
	if err := resolveAtFiles(root, flags); err != nil {
		return err
	}

	// If any content-equivalent flag was provided on CLI, skip stdin.
	// Also skip when --old is set without --new — let dispatch give a clear error.
	hasContent := false
	for _, key := range []string{stdinKey, "content", "new_text", "body", "delete", "move_after"} {
		if _, ok := flags[key]; ok {
			hasContent = true
			break
		}
	}
	if _, hasOld := flags["old_text"]; hasOld && !hasContent {
		hasContent = true // skip stdin, let dispatch validate
	}
	// Handle --content - (read from stdin)
	if v, ok := flags["content"].(string); ok && v == "-" {
		if err := readStdinToFlags(flags, "content"); err != nil {
			return err
		}
	} else if !hasContent {
		if err := readStdinToFlags(flags, stdinKey); err != nil {
			return err
		}
	}

	db, err := openStore(root)
	if err != nil {
		return err
	}
	defer db.Close()

	edrDir := db.EdrDir()
	sess, saveSess := session.LoadSession(edrDir, db.Root())
	defer saveSess()

	injectSessionHash(sess, cmdName, args, flags)

	// Auto-checkpoint before mutations (rolling cap of 3)
	dryRun, _ := flags["dry_run"].(bool)
	if !dryRun && sess != nil {
		dirtyFiles := sess.GetDirtyFiles()
		// Include the current target file so first-edit-in-session is undoable
		var target string
		if len(args) > 0 {
			target = args[0]
			if idx := strings.Index(target, ":"); idx > 0 {
				target = target[:idx]
			}
			found := false
			for _, f := range dirtyFiles {
				if f == target {
					found = true
					break
				}
			}
			if !found {
				dirtyFiles = append(dirtyFiles, target)
			}
		}
		label := cmdName
		if len(args) > 0 {
			label = cmdName + "_" + args[0]
		}
		sessDir := filepath.Join(edrDir, "sessions")
		if _, err := sess.CreateAutoCheckpoint(sessDir, root, label, dirtyFiles); err != nil {
			// Log checkpoint failure but don't block the edit
			fmt.Fprintf(os.Stderr, "edr: checkpoint failed: %v\n", err)
		}
		// Stage pre-mutation content of this op's target into the active
		// transaction's checkpoint so `txn diff` / `txn rollback` see it.
		if sess.ActiveTxn != "" && target != "" {
			_ = sess.AppendFilesToCheckpoint(sessDir, root, sess.ActiveTxn, []string{target})
		}
	}

	env := output.NewEnvelope(cmdName)
	opID := cmdName[:1] + "0"

	result, err := dispatch.Dispatch(context.Background(), db, cmdName, args, flags)
	if err != nil {
		addDispatchFailedOp(env, opID, cmdName, err, sess)
		env.ComputeOK()
		output.PrintEnvelope(env)
		return silentError{code: 1}
	}

	// Apply session post-processing
	data, marshalErr := json.Marshal(result)
	if marshalErr == nil {
		processed := sess.PostProcess(cmdName, args, flags, result, string(data))
		if processed != string(data) {
			var postResult any
			json.Unmarshal([]byte(processed), &postResult)
			result = postResult
		}
	}

	env.AddOp(opID, cmdName, result)

	// Verify after edits only when explicitly requested (--verify / -V).
	// Default is no verify — agents batch verify with the next read or explicitly.
	if cmdName == "edit" || cmdName == "write" {
		dryRun, _ := flags["dry_run"].(bool)
		doVerify, _ := flags["verify"].(bool)
		status, _ := resultStatus(result)
		if !dryRun && doVerify && status == "applied" {
			verifyFlags := map[string]any{}
			if len(args) > 0 {
				verifyFlags["files"] = []string{args[0]}
			} else if rm, ok := result.(map[string]any); ok {
				if f, ok := rm["file"].(string); ok {
					verifyFlags["files"] = []string{f}
				}
			}
			if vc, _ := flags["verify_command"].(string); vc != "" {
				verifyFlags["command"] = vc
			}
			if vl, _ := flags["verify_level"].(string); vl != "" {
				verifyFlags["level"] = vl
			}
			verifyResult, verifyErr := dispatch.Dispatch(context.Background(), db, "verify", []string{}, verifyFlags)
			if verifyErr != nil {
				env.SetVerify(map[string]any{"status": "failed", "error": verifyErr.Error()})
			} else {
				env.SetVerify(verifyResult)
			}
			// Auto-undo on verify failure: restore the pre-edit checkpoint
			// so agents don't proceed with broken code.
			verifyStatus := ""
			if vm, ok := env.Verify.(map[string]any); ok {
				verifyStatus, _ = vm["status"].(string)
			}
			if verifyStatus == "failed" {
				sessDir := filepath.Join(edrDir, "sessions")
				cpID := session.LatestAutoCheckpoint(sessDir)
				if cpID != "" {
					dirtyFiles := sess.GetDirtyFiles()
					if _, _, _, restoreErr := sess.RestoreCheckpoint(sessDir, root, cpID, false, dirtyFiles); restoreErr == nil {
						session.DropCheckpoint(sessDir, cpID)
						if vm, ok := env.Verify.(map[string]any); ok {
							vm["auto_undone"] = true
						}
						// Update the existing op result to reflect the undo
						if len(env.Ops) > 0 {
							lastOp := env.Ops[len(env.Ops)-1]
							lastOp["status"] = "reverted"
							lastOp["msg"] = "edit applied then reverted: verify failed"
							delete(lastOp, "read_back")
						}
					}
				}
			}
		} else if dryRun {
			env.SetVerify(map[string]any{"status": "skipped", "reason": "dry run"})
		}
		// Record auto-verify build state
		if vm, ok := env.Verify.(map[string]any); ok {
			if vs, ok := vm["status"].(string); ok && vs != "skipped" {
				sess.RecordVerify(vs)
			}
		}
	}

	env.ComputeOK()

	// Record op in session log
	recordOp(sess, cmdName, args, flags, result, err == nil)

	output.PrintEnvelope(env)
	if !env.OK {
		return silentError{code: 1}
	}
	return nil
}

// resultStatus extracts the "status" field from a dispatch result.
func resultStatus(result any) (string, bool) {
	if m, ok := result.(map[string]any); ok {
		if s, ok := m["status"].(string); ok {
			return s, true
		}
	}
	// Handle struct types via JSON round-trip
	data, err := json.Marshal(result)
	if err != nil {
		return "", false
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return "", false
	}
	if s, ok := m["status"].(string); ok {
		return s, true
	}
	return "", false
}
