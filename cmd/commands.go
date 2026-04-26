package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/cmdspec"
	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	scopestore "github.com/jordw/edr/internal/scope/store"
	"github.com/jordw/edr/internal/session"
	"github.com/jordw/edr/internal/status"
	"github.com/jordw/edr/internal/warnings"
	"github.com/spf13/cobra"
)

func init() {
	// Primary commands
	rootCmd.AddCommand(orientCmd)
	rootCmd.AddCommand(focusCmd)
	rootCmd.AddCommand(editCmd)
	rootCmd.AddCommand(renameCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(undoCmd)
	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(indexCmd)
	rootCmd.AddCommand(filesCmd)
}

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

// =====================================================================
// Commands
// =====================================================================

var orientCmd = &cobra.Command{
	Use:   "orient [path]",
	Short: ToolDesc["orient"],
	Args:  cobra.MaximumNArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "orient", args) },
}

func init() { cmdspec.RegisterFlags(orientCmd.Flags(), "orient") }

var focusCmd = &cobra.Command{
	Use:   "focus <file>[:<symbol>] [<file>...] [flags]",
	Short: ToolDesc["focus"],
	Args:  cobra.MinimumNArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "focus", args) },
}

func init() {
	cmdspec.RegisterFlags(focusCmd.Flags(), "focus")
	if expand := focusCmd.Flags().Lookup("expand"); expand != nil {
		expand.NoOptDefVal = "deps"
	}
}

var editCmd = &cobra.Command{
	Use:   "edit [file[:symbol]]",
	Short: ToolDesc["edit"],
	Args: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("where") && len(args) == 0 {
			return nil
		}
		if len(args) >= 1 && len(args) <= 2 {
			return nil
		}
		// If --content is set, this is write mode — need exactly 1 file arg
		if cmd.Flags().Changed("content") || cmd.Flags().Changed("inside") || cmd.Flags().Changed("after") {
			if len(args) == 1 {
				return nil
			}
		}
		return fmt.Errorf("accepts between 1 and 2 arg(s), received %d", len(args))
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return dispatchCmdWithStdin(cmd, "edit", args, "new_text")
	},
}

func init() { cmdspec.RegisterFlags(editCmd.Flags(), "edit") }

var renameCmd = &cobra.Command{
	Use:   "rename <file:symbol> --to <new_name>",
	Short: ToolDesc["rename"],
	Args:  cobra.RangeArgs(1, 2),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "rename", args) },
}

func init() { cmdspec.RegisterFlags(renameCmd.Flags(), "rename") }

var statusCmd = &cobra.Command{
	Use:     "status",
	Aliases: []string{"context"},
	Short:   "Session status: build state, stale assumptions, external changes",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		edrDir := index.HomeEdrDir(root)

		sess, saveSess := session.LoadSession(edrDir, root)
		defer saveSess()

		flags := extractFlags(cmd)

		// Handle --reset: clear session and checkpoints
		if cmd.Flags().Changed("reset") {
			id := session.GenerateID()
			newSess := session.New()
			path := filepath.Join(edrDir, "sessions", id+".json")
			os.MkdirAll(filepath.Join(edrDir, "sessions"), 0700)
			newSess.SaveToFile(path)
			session.WriteSessionMapping(filepath.Join(edrDir, "sessions"), id)
			os.RemoveAll(filepath.Join(edrDir, "checkpoints"))
			cleanEdrDir(edrDir)
			result := map[string]any{"status": "reset", "session": id}
			env := output.NewEnvelope("status")
			env.AddOp("s0", "reset", result)
			env.ComputeOK()
			output.PrintEnvelope(env)
			return nil
		}

		// Handle --focus: set/clear focus string
		if cmd.Flags().Changed("focus") {
			focusVal, _ := flags["focus"].(string)
			sess.SetFocus(focusVal)
		}

		// Open DB for assumption checking (best-effort — status works without it)
		var db index.SymbolStore
		db, _ = openStore(root)
		if db != nil {
			defer db.Close()
		}

		result := buildNextResult(sess, db, root, edrDir)

		if flagBool, _ := flags["debug"].(bool); flagBool {
			sessDir := filepath.Join(edrDir, "sessions")
			sessionID := session.ResolveSessionID()
			result["debug"] = map[string]any{
				"root":       root,
				"edr_dir":    edrDir,
				"sess_dir":   sessDir,
				"session_id": sessionID,
				"sess_file":  filepath.Join(sessDir, sessionID+".json"),
				"checkpoints": func() []string {
					infos := session.ListCheckpoints(sessDir)
					ids := make([]string, len(infos))
					for i, c := range infos {
						ids[i] = c.ID
					}
					return ids
				}(),
			}
		}

		env := output.NewEnvelope("status")
		env.AddOp("s0", "status", result)
		env.ComputeOK()
		output.PrintEnvelope(env)
		return nil
	},
}

func init() { cmdspec.RegisterFlags(statusCmd.Flags(), "status") }

// sessionCmd is a hidden backward-compatibility command.
// "edr session new" is now "edr reset --session".
var sessionCmd = &cobra.Command{
	Use:    "session",
	Short:  "Manage sessions (use reset --session instead)",
	Hidden: true,
}

var sessionNewCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a new session (use reset --session instead)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		edrDir := index.HomeEdrDir(root)
		sessDir := filepath.Join(edrDir, "sessions")
		os.MkdirAll(sessDir, 0700)
		id := session.GenerateID()
		sess := session.New()
		path := filepath.Join(sessDir, id+".json")
		if err := sess.SaveToFile(path); err != nil {
			return err
		}
		session.WriteSessionMapping(filepath.Join(edrDir, "sessions"), id)
		fmt.Printf("{\"id\":%q}\n", id)
		cleanEdrDir(edrDir)
		return nil
	},
}

func init() {
	sessionCmd.AddCommand(sessionNewCmd)
	rootCmd.AddCommand(sessionCmd)
}

var undoCmd = &cobra.Command{
	Use:   "undo",
	Short: "Revert to the last auto-checkpoint",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		edrDir := index.HomeEdrDir(root)
		sessDir := filepath.Join(edrDir, "sessions")

		sess, saveSess := session.LoadSession(edrDir, root)
		defer saveSess()

		flags := extractFlags(cmd)
		noSave, _ := flags["no_save"].(bool)

		cpID := session.LatestAutoCheckpoint(sessDir)
		if cpID == "" {
			env := output.NewEnvelope("undo")
			env.AddFailedOpWithCode("u0", "undo", "no_checkpoint", "no auto-checkpoint found; nothing to undo")
			env.ComputeOK()
			output.PrintEnvelope(env)
			return silentError{code: 1}
		}

		dirtyFiles := sess.GetDirtyFiles()
		restored, notRemoved, preRestoreID, err := sess.RestoreCheckpoint(
			sessDir, root, cpID, !noSave, dirtyFiles,
		)
		if err != nil {
			return err
		}

		// Drop the auto-checkpoint we just restored (it is consumed)
		session.DropCheckpoint(sessDir, cpID)

		result := map[string]any{
			"status":   "undone",
			"target":   cpID,
			"restored": restored,
		}
		if preRestoreID != "" {
			result["safety_checkpoint"] = preRestoreID
		}
		// Report remaining checkpoints so the agent knows how many undos are left
		remaining := session.ListCheckpoints(sessDir)
		// Filter to only auto checkpoints (cp_auto_*)
		autoCount := 0
		for _, cp := range remaining {
			if strings.HasPrefix(cp.ID, "cp_auto_") {
				autoCount++
			}
		}
		result["remaining"] = autoCount
		// Files modified after the checkpoint that weren't snapshotted.
		// Do NOT delete them — they may be pre-existing files that a
		// multi-file rename modified. Only files with nil content in the
		// checkpoint (truly new files) are deleted by RestoreCheckpoint itself.
		if len(notRemoved) > 0 {
			result["unrestored"] = notRemoved
		}

		env := output.NewEnvelope("undo")
		env.AddOp("u0", "undo", result)
		env.ComputeOK()
		output.PrintEnvelope(env)
		return nil
	},
}

func init() { cmdspec.RegisterFlags(undoCmd.Flags(), "undo") }

var indexCmd = &cobra.Command{
	Use:   "index",
	Short: ToolDesc["index"],
	Args:  cobra.NoArgs,
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "index", args) },
}

func init() { cmdspec.RegisterFlags(indexCmd.Flags(), "index") }

var filesCmd = &cobra.Command{
	Use:   "files <pattern>",
	Short: ToolDesc["files"],
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "files", args) },
}

func init() { cmdspec.RegisterFlags(filesCmd.Flags(), "files") }

var refsToCmd = &cobra.Command{
	Use:   "refs-to <file:Symbol>",
	Short: ToolDesc["refs-to"],
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "refs-to", args) },
}

func init() {
	rootCmd.AddCommand(refsToCmd)
	cmdspec.RegisterFlags(refsToCmd.Flags(), "refs-to")
}

var benchCmd = &cobra.Command{
	Use:   "bench",
	Short: ToolDesc["bench"],
	Args:  cobra.NoArgs,
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "bench", args) },
}

func init() { rootCmd.AddCommand(benchCmd) }

// buildNextResult constructs the result map for `edr next`.
func buildNextResult(sess *session.Session, db index.SymbolStore, root, edrDir string) map[string]any {
	result := map[string]any{}

	// Always show root so agents know which repo context they're in.
	result["root"] = output.Rel(root)

	// Storage health: index + scope + session share a uniform Reporter
	// contract in internal/status. Each block below is populated from an
	// Aggregate call; the legacy "index" shape (files, complete, symbols)
	// is preserved for agents that already depend on it, while scope and
	// session are additive.
	reports := status.Aggregate(
		idx.NewReporter(root, edrDir),
		scopestore.NewReporter(edrDir),
		session.NewReporter(edrDir),
	)
	for _, rep := range reports {
		switch rep.Name {
		case "index":
			idxInfo := map[string]any{
				"files":    rep.Files,
				"complete": idx.IsComplete(root, edrDir),
			}
			if syms, ok := rep.Extra["symbols"].(int); ok {
				idxInfo["symbols"] = syms
			}
			result["index"] = idxInfo
		case "scope":
			if rep.Exists {
				result["scope"] = map[string]any{
					"files": rep.Files,
					"bytes": rep.Bytes,
				}
			}
		case "session":
			if rep.Exists {
				sessInfo := map[string]any{
					"files": rep.Files,
					"bytes": rep.Bytes,
				}
				if cp, ok := rep.Extra["checkpoints"].(int); ok {
					sessInfo["checkpoints"] = cp
				}
				result["session"] = sessInfo
			}
		}
	}

	// Undo availability
	sessDir := filepath.Join(edrDir, "sessions")
	cpID := session.LatestAutoCheckpoint(sessDir)
	if cpID != "" {
		result["undo_available"] = true
	}
	// Focus
	if focus := sess.GetFocus(); focus != "" {
		result["focus"] = focus
	}

	// Build state
	buildStatus, editsSince := sess.BuildState()
	if buildStatus != "" {
		build := map[string]any{"status": buildStatus}
		if editsSince {
			build["edits_since"] = true
		}
		result["build"] = build
	}

	// Stale assumptions (fix items)
	if db != nil {
		fix := computeFixItems(sess, db)
		if len(fix) > 0 {
			result["fix"] = fix
		}
	}

	// External file modifications
	extMods := warnings.Check(sess, root)
	if len(extMods) > 0 {
		var items []any
		for _, w := range extMods {
			items = append(items, map[string]any{
				"file":    w.File,
				"kind":    w.Kind,
				"since":   w.OpID,
				"message": w.Message,
			})
		}
		result["external_changes"] = items
	}

	return result
}

// computeStaleAssumptions resolves current signatures for all tracked assumptions
// and returns any that have become stale. Shared by computeFixItems and emitWarnings.
func computeStaleAssumptions(sess *session.Session, db index.SymbolStore) []session.StaleAssumption {
	assumptions := sess.GetAssumptions()
	if len(assumptions) == 0 {
		return nil
	}

	currentSigs := make(map[string]string, len(assumptions))
	ctx := context.Background()
	for key := range assumptions {
		idx := strings.IndexByte(key, ':')
		if idx <= 0 {
			continue
		}
		file, symName := key[:idx], key[idx+1:]

		absFile, err := db.ResolvePath(file)
		if err != nil {
			continue
		}
		syms, err := db.GetSymbolsByFile(ctx, absFile)
		if err != nil {
			continue
		}
		src, err := os.ReadFile(absFile)
		if err != nil {
			continue
		}
		for _, sym := range syms {
			if sym.Name == symName {
				sig := index.ExtractSignatureFromSource(sym, src)
				currentSigs[key] = session.SigHash(sig)
				break
			}
		}
	}

	return sess.CheckAssumptions(currentSigs)
}

func computeFixItems(sess *session.Session, db index.SymbolStore) []any {
	stale := computeStaleAssumptions(sess, db)
	if len(stale) == 0 {
		return nil
	}

	var fix []any
	for i, s := range stale {
		item := map[string]any{
			"id":         fmt.Sprintf("stale_%d", i+1),
			"type":       "stale_assumption",
			"confidence": "exact",
			"file":       s.File,
			"symbol":     s.Symbol,
			"assumed_at": s.AssumedAt,
			"suggest":    fmt.Sprintf("read %s:%s", s.File, s.Symbol),
		}
		if s.Current == "" {
			item["reason"] = "symbol no longer exists"
		} else {
			item["reason"] = "signature changed since read"
		}
		fix = append(fix, item)
	}
	return fix
}

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

// addDispatchFailedOp creates a failed op on the envelope, matching batch behavior.
// Per-op errors go on the op; only index-level errors go in envelope errors[].
func addDispatchFailedOp(env *output.Envelope, opID, opType string, err error, sess *session.Session) {
	// Surface structured not-found errors with diagnostic hints
	var nfe *dispatch.NotFoundError
	if errors.As(err, &nfe) {
		// Attribute staleness to the agent's own prior edits when applicable.
		if sess != nil && nfe.File != "" {
			if n := sess.EditsSinceRead(nfe.File); n > 0 {
				nfe.EditsAgo = n
				stale := fmt.Sprintf("your view is stale: %d edit(s) to %s since last read — run `edr focus %s` first", n, nfe.File, nfe.File)
				if nfe.Hint == "" {
					nfe.Hint = stale
				} else {
					nfe.Hint = stale + "; " + nfe.Hint
				}
			}
		}
		env.AddFailedOpResult(opID, opType, "not_found", nfe)
		return
	}

	// Surface ambiguous symbol errors with candidates
	var ambErr *index.AmbiguousSymbolError
	if errors.As(err, &ambErr) {
		candidates := make([]map[string]any, len(ambErr.Candidates))
		for i, c := range ambErr.Candidates {
			rel := c.File
			if ambErr.Root != "" {
				rel = output.Rel(c.File)
			}
			candidates[i] = map[string]any{
				"file": rel,
				"line": c.StartLine,
				"type": c.Type,
			}
		}
		env.AddFailedOpResult(opID, opType, "ambiguous_symbol", map[string]any{
			"error":      ambErr.Error(),
			"symbol":     ambErr.Name,
			"candidates": candidates,
			"hint":       "use file:symbol to disambiguate",
		})
		return
	}

	// Classify remaining op-level errors with specific codes
	code := classifyError(err)
	env.AddFailedOpWithCode(opID, opType, code, err.Error())
}

// classifyError maps dispatch errors to structured error codes.
func classifyError(err error) string {
	var nfe *dispatch.NotFoundError
	if errors.As(err, &nfe) {
		return "not_found"
	}
	var ambErr *index.AmbiguousSymbolError
	if errors.As(err, &ambErr) {
		return "ambiguous_symbol"
	}
	return classifyErrorMsg(err.Error())
}

// classifyErrorMsg classifies an error message string into a structured code.
func classifyErrorMsg(msg string) string {
	switch {
	case strings.Contains(msg, "not found"):
		return "not_found"
	case strings.Contains(msg, "is ambiguous"):
		return "ambiguous_symbol"
	case strings.Contains(msg, "ambiguous"):
		return "ambiguous_match"
	case strings.Contains(msg, "no such file"):
		return "file_not_found"
	case strings.Contains(msg, "outside repo root"):
		return "outside_repo"
	case strings.Contains(msg, "hash mismatch"):
		return "hash_mismatch"
	case strings.Contains(msg, "mutually exclusive"):
		return "invalid_mode"
	default:
		return "command_error"
	}
}

// extractAndStripSignature extracts _signature from a dispatch result, records
// the assumption in the session, and removes the internal field before any
// serialization can leak it.
func extractAndStripSignature(sess *session.Session, cmdName string, args []string, result any) {
	if cmdName != "read" && cmdName != "focus" {
		return
	}
	m, ok := result.(map[string]any)
	if !ok {
		return
	}
	sig, hasSig := m["_signature"].(string)
	delete(m, "_signature") // always strip, even if empty
	if !hasSig || sig == "" {
		return
	}
	_, symbol := extractFileSymbol(args)
	if symbol == "" {
		return
	}
	file, _ := extractFileSymbol(args)
	key := file + ":" + symbol
	// Use a placeholder op ID — recordOp hasn't run yet.
	// We'll use "r?" and it gets overwritten on the next read anyway.
	sess.RecordAssumption(key, sig, "r?")
}

// recordOp extracts file/symbol from args and records an op in the session log.
// Also handles build state tracking.
func recordOp(sess *session.Session, cmdName string, args []string, flags map[string]any, result any, ok bool) {
	file, symbol := extractFileSymbol(args)
	// Enrich symbol from --in flag if not already set (e.g. edit main.go --in hello)
	if symbol == "" {
		if inFlag, ok := flags["in"].(string); ok && inFlag != "" {
			symbol = inFlag
		}
	}
	action, kind := classifyOp(cmdName, flags, result, ok)
	sess.RecordOp(cmdName, file, symbol, action, kind, ok)

	// Multi-file rename can modify files beyond the primary
	// target. Record extra ops so GetDirtyFiles includes them in checkpoints.
	if ok && cmdName == "rename" {
		extraFiles := multiFileEdits(cmdName, result, file)
		for _, f := range extraFiles {
			sess.RecordOp(cmdName, f, "", action, kind, true)
		}
	}

	if !ok {
		return
	}

	m, isMap := result.(map[string]any)
	if !isMap {
		return
	}

	// Update assumption op ID now that we have the real one
	if (cmdName == "read" || cmdName == "focus") && symbol != "" {
		key := file + ":" + symbol
		ops := sess.GetRecentOps(1)
		if len(ops) > 0 {
			sess.UpdateAssumptionOpID(key, ops[0].OpID)
		}
	}

	// Track build state
	switch cmdName {
	case "edit", "write":
		status, _ := m["status"].(string)
		if status == "applied" || status == "applied_index_stale" {
			sess.RecordEdit()
		}
	case "verify":
		if status, sOk := m["status"].(string); sOk {
			sess.RecordVerify(status)
		}
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

// extractFileSymbol parses file and optional symbol from command args.
// multiFileEdits returns extra files modified by multi-file commands (rename, changesig)
// that are not the primary target file. This ensures GetDirtyFiles tracks them for checkpoints.
func multiFileEdits(cmdName string, result any, primaryFile string) []string {
	switch cmdName {
	case "rename":
		if r, ok := result.(*output.RenameResult); ok {
			var extra []string
			for _, f := range r.FilesChanged {
				if f != primaryFile {
					extra = append(extra, f)
				}
			}
			return extra
		}
	}
	return nil
}

func extractFileSymbol(args []string) (file, symbol string) {
	if len(args) == 0 {
		return "", ""
	}
	arg := args[0]
	if idx := strings.IndexByte(arg, ':'); idx > 0 {
		return arg[:idx], arg[idx+1:]
	}
	return arg, ""
}

// classifyOp determines the action and display kind for an operation.
func classifyOp(cmd string, flags map[string]any, result any, ok bool) (action, kind string) {
	if !ok {
		return cmd + "_failed", cmd + "_failed"
	}
	switch cmd {
	case "read":
		if _, hasSig := flags["signatures"]; hasSig {
			return "read_signatures", "signatures_read"
		}
		if _, hasSkel := flags["skeleton"]; hasSkel {
			return "read_skeleton", "skeleton_read"
		}
		return "read_symbol", "symbol_read"
	case "edit":
		if _, hasDel := flags["delete"]; hasDel {
			return "delete", "symbol_deleted"
		}
		return "replace_text", "text_replaced"
	case "write":
		return "write_file", "file_written"
	case "search":
		return "search", "search"
	case "verify":
		// Check verify result status
		if m, mOk := result.(map[string]any); mOk {
			if status, sOk := m["status"].(string); sOk && status == "passed" {
				return "verify", "verify_passed"
			}
		}
		return "verify", "verify_failed"
	case "rename":
		return "rename", "renamed"
	case "map":
		return "map", "map_viewed"
	default:
		return cmd, cmd
	}
}
