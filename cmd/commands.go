package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strings"

	"github.com/jordw/edr/internal/cmdspec"
	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/session"
	"github.com/jordw/edr/internal/warnings"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(readCmd)
	rootCmd.AddCommand(writeCmd)
	rootCmd.AddCommand(editCmd)
	rootCmd.AddCommand(mapCmd)
	rootCmd.AddCommand(searchCmd)
	rootCmd.AddCommand(refsCmd)
	rootCmd.AddCommand(renameCmd)
	rootCmd.AddCommand(prepareCmd)
	rootCmd.AddCommand(verifyCmd)
	rootCmd.AddCommand(resetCmd)
	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(undoCmd)
}

// dispatchCmdWithIndex is like dispatchCmd but auto-indexes if needed.
// Used only by reindex/init.
func dispatchCmdWithIndex(cmd *cobra.Command, cmdName string, args []string) error {
	flags := extractFlags(cmd)

	db, err := openDBAndIndex(getRoot(cmd), !verbose)
	if err != nil {
		return err
	}
	defer db.Close()

	env := output.NewEnvelope(cmdName)

	opID := cmdName[:1] + "0"
	result, err := dispatch.Dispatch(context.Background(), db, cmdName, args, flags)
	if err != nil {
		addDispatchFailedOp(env, opID, cmdName, err)
	} else {
		env.AddOp(opID, cmdName, result)
	}
	env.ComputeOK()
	output.PrintEnvelope(env)
	return nil
}

// dispatchCmd is the common pattern: open DB, dispatch, wrap in envelope, print.
// Loads a file-backed session when EDR_SESSION is set.
func dispatchCmd(cmd *cobra.Command, cmdName string, args []string) error {
	root := getRoot(cmd)
	flags := extractFlags(cmd)
	if err := resolveAtFiles(root, flags); err != nil {
		return err
	}

	db, err := openDBStrictRoot(root)
	if err != nil {
		return err
	}
	defer db.Close()

	edrDir := db.EdrDir()
	sess, saveSess := session.LoadSession(edrDir)
	defer saveSess()

	env := output.NewEnvelope(cmdName)
	opID := cmdName[:1] + "0"

	result, err := dispatch.Dispatch(context.Background(), db, cmdName, args, flags)
	if err != nil {
		addDispatchFailedOp(env, opID, cmdName, err)
		env.ComputeOK()
		output.PrintEnvelope(env)
		return nil
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
	env.ComputeOK()

	// Record op in session log
	recordOp(sess, cmdName, args, flags, result, err == nil)

	output.PrintEnvelope(env)
	return nil
}

// dispatchCmdWithStdin is like dispatchCmd but reads stdin into a flag first.
func dispatchCmdWithStdin(cmd *cobra.Command, cmdName string, args []string, stdinKey string) error {
	root := getRoot(cmd)
	flags := extractFlags(cmd)
	if err := resolveAtFiles(root, flags); err != nil {
		return err
	}

	// If any content-equivalent flag was provided on CLI, skip stdin.
	hasContent := false
	for _, key := range []string{stdinKey, "content", "new_text", "body", "delete", "move_after"} {
		if _, ok := flags[key]; ok {
			hasContent = true
			break
		}
	}
	if !hasContent {
		if err := readStdinToFlags(flags, stdinKey); err != nil {
			return err
		}
	}

	db, err := openDBStrictRoot(root)
	if err != nil {
		return err
	}
	defer db.Close()

	edrDir := db.EdrDir()
	sess, saveSess := session.LoadSession(edrDir)
	defer saveSess()

	injectSessionHash(sess, cmdName, args, flags)

	// Auto-checkpoint before mutations (rolling cap of 3)
	dryRun, _ := flags["dry_run"].(bool)
	if !dryRun && sess != nil {
		dirtyFiles := sess.GetDirtyFiles()
		// Include the current target file so first-edit-in-session is undoable
		if len(args) > 0 {
			target := args[0]
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
		sess.CreateAutoCheckpoint(filepath.Join(edrDir, "sessions"), root, label, dirtyFiles)
	}

	env := output.NewEnvelope(cmdName)
	opID := cmdName[:1] + "0"

	result, err := dispatch.Dispatch(context.Background(), db, cmdName, args, flags)
	if err != nil && strings.Contains(err.Error(), "hash mismatch") && sess != nil && len(args) > 0 {
		// Auto-refresh: external modification made the session hash stale.
		// Re-read the file hash from disk and retry once.
		target := args[0]
		if idx := strings.Index(target, ":"); idx > 0 {
			target = target[:idx]
		}
		if resolved, resolveErr := db.ResolvePath(target); resolveErr == nil {
			if currentHash, hashErr := edit.FileHash(resolved); hashErr == nil {
				sess.RefreshFileHash(target, currentHash)
				flags["expect_hash"] = currentHash
				result, err = dispatch.Dispatch(context.Background(), db, cmdName, args, flags)
			}
		}
	}
	if err != nil {
		addDispatchFailedOp(env, opID, cmdName, err)
		env.ComputeOK()
		output.PrintEnvelope(env)
		return nil
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

	// Auto-verify after successful mutations (parity with batch mode)
	if cmdName == "edit" || cmdName == "write" {
		dryRun, _ := flags["dry_run"].(bool)
		status, _ := resultStatus(result)
		if !dryRun && status == "applied" {
			verifyFlags := map[string]any{}
			if len(args) > 0 {
				verifyFlags["files"] = []string{args[0]}
			} else if rm, ok := result.(map[string]any); ok {
				if f, ok := rm["file"].(string); ok {
					verifyFlags["files"] = []string{f}
				}
			}
			verifyResult, verifyErr := dispatch.Dispatch(context.Background(), db, "verify", []string{}, verifyFlags)
			if verifyErr != nil {
				env.SetVerify(map[string]any{"status": "failed", "error": verifyErr.Error()})
			} else {
				env.SetVerify(verifyResult)
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

var readCmd = &cobra.Command{
	Use:   "read <file>[:<symbol>] [<file>...] [flags]",
	Short: ToolDesc["read"],
	Args:  cobra.MinimumNArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "read", args) },
}

func init() {
	cmdspec.RegisterFlags(readCmd.Flags(), "read")
	// Allow bare --expand (no value) to default to "deps"
	if f := readCmd.Flags().Lookup("expand"); f != nil {
		f.NoOptDefVal = "deps"
	}
}

var writeCmd = &cobra.Command{
	Use:   "write <file>",
	Short: ToolDesc["write"],
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return dispatchCmdWithStdin(cmd, "write", args, "content")
	},
}

func init() { cmdspec.RegisterFlags(writeCmd.Flags(), "write") }

var editCmd = &cobra.Command{
	Use:   "edit [file[:symbol]]",
	Short: ToolDesc["edit"],
	Args: func(cmd *cobra.Command, args []string) error {
		// --where replaces the file argument — allow 0 args
		if cmd.Flags().Changed("where") && len(args) == 0 {
			return nil
		}
		if len(args) >= 1 && len(args) <= 2 {
			return nil
		}
		for _, a := range args {
			if a == "edit" {
				return fmt.Errorf("multiple edits: use batch syntax instead: edr -e file1 --old ... --new ... -e file2 --old ... --new ...")
			}
		}
		return fmt.Errorf("accepts between 1 and 2 arg(s), received %d", len(args))
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return dispatchCmdWithStdin(cmd, "edit", args, "new_text")
	},
}

func init() { cmdspec.RegisterFlags(editCmd.Flags(), "edit") }

var mapCmd = &cobra.Command{
	Use:   "map [file]",
	Short: ToolDesc["map"],
	Args:  cobra.MaximumNArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "map", args) },
}

func init() { cmdspec.RegisterFlags(mapCmd.Flags(), "map") }

var searchCmd = &cobra.Command{
	Use:   "search <pattern>",
	Short: ToolDesc["search"],
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "search", args) },
}

func init() { cmdspec.RegisterFlags(searchCmd.Flags(), "search") }

var refsCmd = &cobra.Command{
	Use:   "refs <file:symbol|symbol>",
	Short: ToolDesc["refs"],
	Args:  cobra.RangeArgs(1, 2),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "refs", args) },
}

func init() { cmdspec.RegisterFlags(refsCmd.Flags(), "refs") }

var prepareCmd = &cobra.Command{
	Use:   "prepare <file:symbol|symbol>",
	Short: ToolDesc["prepare"],
	Args:  cobra.RangeArgs(1, 2),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "prepare", args) },
}

func init() { cmdspec.RegisterFlags(prepareCmd.Flags(), "prepare") }

var renameCmd = &cobra.Command{
	Use:   "rename <old-name> <new-name>",
	Short: ToolDesc["rename"],
	Args:  cobra.ExactArgs(2),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "rename", args) },
}

func init() { cmdspec.RegisterFlags(renameCmd.Flags(), "rename") }

func init() {
	cmdspec.RegisterFlags(resetCmd.Flags(), "reset")
	resetCmd.Flags().String("cpuprofile", "", "Write CPU profile to file")
	resetCmd.Flags().MarkHidden("cpuprofile")
}

var resetCmd = &cobra.Command{
	Use:     "reset",
	Aliases: []string{"reindex", "init"},
	Short:   ToolDesc["reset"],
	RunE: func(cmd *cobra.Command, args []string) error {
		profPath, _ := cmd.Flags().GetString("cpuprofile")
		if profPath != "" {
			f, err := os.Create(profPath)
			if err != nil {
				return fmt.Errorf("create cpuprofile: %w", err)
			}
			pprof.StartCPUProfile(f)
			defer func() {
				pprof.StopCPUProfile()
				f.Close()
			}()
		}

		flags := extractFlags(cmd)
		indexOnly, _ := flags["index"].(bool)
		sessionOnly, _ := flags["session"].(bool)

		root := getRoot(cmd)
		edrDir := filepath.Join(root, ".edr")
		result := map[string]any{"status": "reset"}

		// Session reset
		if !indexOnly {
			if err := os.MkdirAll(filepath.Join(edrDir, "sessions"), 0700); err != nil {
				return err
			}
			id := session.GenerateID()
			sess := session.New()
			path := filepath.Join(edrDir, "sessions", id+".json")
			if err := sess.SaveToFile(path); err != nil {
				return err
			}
			session.WriteSessionMapping(filepath.Join(edrDir, "sessions"), id)
			result["session"] = id

			if !sessionOnly {
				// Clear checkpoints
				cpDir := filepath.Join(edrDir, "checkpoints")
				os.RemoveAll(cpDir)
			}

			cleanEdrDir(edrDir)
		}

		// Index reset
		if !sessionOnly {
			if err := dispatchCmdWithIndex(cmd, "reindex", args); err != nil {
				return err
			}
			result["scope"] = "full"
			if indexOnly {
				result["scope"] = "index"
			}
		} else {
			result["scope"] = "session"
			env := output.NewEnvelope("reset")
			env.AddOp("r0", "reset", result)
			env.ComputeOK()
			output.PrintEnvelope(env)
		}

		return nil
	},
}

var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: ToolDesc["verify"],
	RunE: func(cmd *cobra.Command, args []string) error {
		flags := extractFlags(cmd)

		// Verify does not need the symbol index or .edr directory.
		// Resolve root without opening a DB to avoid creating .edr as a side effect.
		root := getRoot(cmd)
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return err
		}
		output.SetRoot(absRoot)

		// Load session for build state tracking (only if .edr exists — verify shouldn't create it)
		edrDir := filepath.Join(absRoot, ".edr")
		var sess *session.Session
		var saveSess func()
		if _, statErr := os.Stat(edrDir); statErr == nil {
			sess, saveSess = session.LoadSession(edrDir)
			defer saveSess()
		}

		env := output.NewEnvelope("verify")

		result, dispErr := dispatch.DispatchVerify(context.Background(), absRoot, args, flags)
		if dispErr != nil {
			env.SetVerify(map[string]any{"status": "failed", "error": dispErr.Error()})
		} else {
			env.SetVerify(result)
		}

		// Record verify in session
		if sess != nil {
			if vm, ok := env.Verify.(map[string]any); ok {
				if status, ok := vm["status"].(string); ok && status != "skipped" {
					sess.RecordVerify(status)
					recordOp(sess, "verify", args, flags, vm, true)
				}
			}
		}

		env.ComputeOK()
		output.PrintEnvelope(env)
		return nil
	},
}

func init() { cmdspec.RegisterFlags(verifyCmd.Flags(), "verify") }

var statusCmd = &cobra.Command{
	Use:     "status",
	Aliases: []string{"context"},
	Short:   "Session status: recent ops, build state, action items",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		edrDir := filepath.Join(root, ".edr")

		sess, saveSess := session.LoadSession(edrDir)
		defer saveSess()

		flags := extractFlags(cmd)

		// Handle --focus: set/clear focus string
		if cmd.Flags().Changed("focus") {
			focusVal, _ := flags["focus"].(string)
			sess.SetFocus(focusVal)
		}

		count := 10
		if v, ok := flags["count"].(int); ok && v > 0 {
			count = v
		}

		// Open DB for assumption checking (best-effort — status works without it)
		var db *index.DB
		db, _ = openDBStrictRoot(root)
		if db != nil {
			defer db.Close()
		}

		result := buildNextResult(sess, db, root, count)
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
		edrDir := filepath.Join(root, ".edr")
		if err := os.MkdirAll(filepath.Join(edrDir, "sessions"), 0700); err != nil {
			return err
		}
		id := session.GenerateID()
		sess := session.New()
		path := filepath.Join(edrDir, "sessions", id+".json")
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
		edrDir := filepath.Join(root, ".edr")
		sessDir := filepath.Join(edrDir, "sessions")

		sess, saveSess := session.LoadSession(edrDir)
		defer saveSess()

		flags := extractFlags(cmd)
		noSave, _ := flags["no_save"].(bool)

		cpID := session.LatestAutoCheckpoint(sessDir)
		if cpID == "" {
			env := output.NewEnvelope("undo")
			env.AddFailedOpWithCode("u0", "undo", "no_checkpoint", "no auto-checkpoint found; nothing to undo")
			env.ComputeOK()
			output.PrintEnvelope(env)
			return nil
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
			"restored": restored,
			"target":   cpID,
		}
		if preRestoreID != "" {
			result["safety_checkpoint"] = preRestoreID
		}
		if len(notRemoved) > 0 {
			result["new_files_kept"] = notRemoved
		}

		env := output.NewEnvelope("undo")
		env.AddOp("u0", "undo", result)
		env.ComputeOK()
		output.PrintEnvelope(env)
		return nil
	},
}

func init() { cmdspec.RegisterFlags(undoCmd.Flags(), "undo") }

// buildNextResult constructs the result map for `edr next`.
func buildNextResult(sess *session.Session, db *index.DB, root string, count int) map[string]any {
	result := map[string]any{}

	// Focus
	if focus := sess.GetFocus(); focus != "" {
		result["focus"] = focus
	}

	// Recent ops (reverse order — most recent first)
	ops := sess.GetRecentOps(count)
	if len(ops) > 0 {
		recent := make([]any, len(ops))
		for i, op := range ops {
			entry := map[string]any{
				"op_id": op.OpID,
				"cmd":   op.Cmd,
				"kind":  op.Kind,
			}
			if op.File != "" {
				entry["file"] = op.File
			}
			if op.Symbol != "" {
				entry["symbol"] = op.Symbol
			}
			if !op.OK {
				entry["ok"] = false
			}
			recent[i] = entry
		}
		result["recent"] = recent
	}

	// Total op count
	allOps := sess.GetRecentOps(0)
	result["total_ops"] = len(allOps)

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

		current := computeCurrentItems(sess, db)
		if len(current) > 0 {
			result["current"] = current
		}
	}

	// Session-pattern suggestions
	if suggestions := analyzePatterns(sess); len(suggestions) > 0 {
		result["suggestions"] = suggestions
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

// analyzePatterns scans the session op log for suboptimal usage patterns
// and returns actionable suggestions for the agent.
func analyzePatterns(sess *session.Session) []string {
	ops := sess.GetRecentOps(0) // all ops
	if len(ops) < 3 {
		return nil // too few ops to detect patterns
	}

	var suggestions []string

	// 1. Full-file reads without --skeleton or --budget
	fullReads := 0
	for _, op := range ops {
		if op.Cmd == "read" && op.OK && op.Action == "read_symbol" && op.Symbol == "" {
			fullReads++
		}
	}
	if fullReads >= 3 {
		suggestions = append(suggestions, fmt.Sprintf(
			"%d full-file reads without --skeleton or --budget — these waste context tokens",
			fullReads))
	}

	// 2. Sequential reads that could be batched — emit the actual command
	suggestions = append(suggestions, suggestBatchReads(ops)...)

	// 3. Edits without a prior refs check on the same symbol
	refsChecked := make(map[string]bool)
	editsWithoutRefs := 0
	for _, op := range ops {
		if op.Cmd == "refs" && op.OK && op.File != "" {
			key := op.File
			if op.Symbol != "" {
				key += ":" + op.Symbol
			}
			refsChecked[key] = true
		}
		if (op.Cmd == "edit" || op.Cmd == "rename") && op.OK && op.Symbol != "" {
			key := op.File + ":" + op.Symbol
			if !refsChecked[key] && !refsChecked[op.File] {
				editsWithoutRefs++
			}
		}
	}
	if editsWithoutRefs >= 2 {
		suggestions = append(suggestions, fmt.Sprintf(
			"%d symbol edits without prior refs check — use edr refs Symbol --impact before refactoring",
			editsWithoutRefs))
	}

	// 4. Edit immediately followed by read of the same file
	suggestions = append(suggestions, suggestReadBack(ops)...)

	return suggestions
}

// suggestBatchReads finds runs of 3+ sequential reads and emits
// the concrete batch command the agent should have used.
func suggestBatchReads(ops []session.OpEntry) []string {
	var suggestions []string
	var run []session.OpEntry

	flushRun := func() {
		if len(run) < 3 {
			run = run[:0]
			return
		}
		var parts []string
		for _, op := range run {
			target := op.File
			if op.Symbol != "" {
				target += ":" + op.Symbol
			}
			parts = append(parts, "-r "+target)
		}
		suggestions = append(suggestions, fmt.Sprintf(
			"%d sequential reads — next time: edr %s",
			len(run), strings.Join(parts, " ")))
		run = run[:0]
	}

	for _, op := range ops {
		if op.Cmd == "read" && op.OK {
			run = append(run, op)
		} else {
			flushRun()
		}
	}
	flushRun()
	return suggestions
}

// suggestReadBack detects edit→read on the same file and suggests --read-back.
func suggestReadBack(ops []session.OpEntry) []string {
	var suggestions []string
	count := 0
	for i := 1; i < len(ops); i++ {
		prev := ops[i-1]
		cur := ops[i]
		if prev.Cmd == "edit" && prev.OK && cur.Cmd == "read" && cur.OK && cur.File == prev.File {
			count++
		}
	}
	if count >= 2 {
		suggestions = append(suggestions, fmt.Sprintf(
			"%d edits followed by reads of the same file — use --read-back on the edit instead",
			count))
	}
	return suggestions
}

// computeStaleAssumptions resolves current signatures for all tracked assumptions
// and returns any that have become stale. Shared by computeFixItems and emitWarnings.
func computeStaleAssumptions(sess *session.Session, db *index.DB) []session.StaleAssumption {
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

func computeFixItems(sess *session.Session, db *index.DB) []any {
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

// MaxCurrentItems is the hard cap on symbols in the current: section.
const MaxCurrentItems = 10

// currentItem represents one symbol in the current: section of next output.
type currentItem struct {
	File   string
	Symbol string
	Reason string // "modified", "stale", "recent"
	Sig    string // current signature from index
}

// computeCurrentItems builds the current: section — live signatures of active symbols.
// Sources (in priority order): modified symbols, stale assumptions, recent symbol reads.
// Deduplicates by file:symbol, caps at MaxCurrentItems.
func computeCurrentItems(sess *session.Session, db *index.DB) []any {
	if db == nil {
		return nil
	}

	type candidate struct {
		file, symbol, reason string
		priority             int // 0 = modified (highest), 1 = stale, 2 = recent
	}

	seen := make(map[string]bool)
	var candidates []candidate

	addCandidate := func(file, symbol, reason string, priority int) {
		if file == "" || symbol == "" {
			return
		}
		key := file + ":" + symbol
		if seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, candidate{file, symbol, reason, priority})
	}

	// 1. Modified symbols: walk op log for edit/write ops with a symbol
	allOps := sess.GetRecentOps(0)
	for i := len(allOps) - 1; i >= 0; i-- {
		op := allOps[i]
		if !op.OK || op.Symbol == "" {
			continue
		}
		switch op.Cmd {
		case "edit", "write":
			addCandidate(op.File, op.Symbol, "modified", 0)
		}
	}

	// 2. Stale assumptions
	assumptions := sess.GetAssumptions()
	// We need current sigs to check staleness — reuse the same logic as computeFixItems
	ctx := context.Background()
	currentSigs := make(map[string]string, len(assumptions))
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
	stale := sess.CheckAssumptions(currentSigs)
	for _, s := range stale {
		addCandidate(s.File, s.Symbol, "stale", 1)
	}

	// 3. Recent symbol-scoped reads (last 20 ops, most recent first)
	recentLimit := 20
	if recentLimit > len(allOps) {
		recentLimit = len(allOps)
	}
	for i := len(allOps) - 1; i >= len(allOps)-recentLimit; i-- {
		if i < 0 {
			break
		}
		op := allOps[i]
		if op.Cmd == "read" && op.Symbol != "" && op.OK {
			addCandidate(op.File, op.Symbol, "recent", 2)
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	// Sort by priority (modified first, then stale, then recent)
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].priority < candidates[j].priority
	})

	// Cap
	if len(candidates) > MaxCurrentItems {
		candidates = candidates[:MaxCurrentItems]
	}

	// Resolve current signatures from index
	var items []any
	for _, c := range candidates {
		absFile, err := db.ResolvePath(c.file)
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
			if sym.Name == c.symbol {
				sig := index.ExtractSignatureFromSource(sym, src)
				sig = trimSignature(sig, c.symbol)
				items = append(items, map[string]any{
					"file":      c.file,
					"symbol":    c.symbol,
					"reason":    c.reason,
					"signature": sig,
				})
				break
			}
		}
	}

	return items
}

// trimSignature cleans up multi-line signatures for compact display.
func trimSignature(sig, symbol string) string {
	if !strings.Contains(sig, "\n") {
		return sig
	}
	lines := strings.Split(sig, "\n")
	// Prefer declaration lines (func/type/class/def/etc.) that contain the symbol name
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, symbol) && !strings.HasPrefix(trimmed, "//") && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "*") {
			return trimmed
		}
	}
	// Fallback: first non-empty, non-comment line
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "//") && !strings.HasPrefix(trimmed, "#") {
			return trimmed
		}
	}
	return strings.TrimSpace(lines[0])
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
func addDispatchFailedOp(env *output.Envelope, opID, opType string, err error) {
	var idxErr *IndexError
	if errors.As(err, &idxErr) {
		// Index errors are envelope-level (not tied to a specific op)
		env.AddError(idxErr.Code, idxErr.Message)
		return
	}

	// Surface structured not-found errors with diagnostic hints
	var nfe *dispatch.NotFoundError
	if errors.As(err, &nfe) {
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
	if cmdName != "read" {
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

	if !ok {
		return
	}

	m, isMap := result.(map[string]any)
	if !isMap {
		return
	}

	// Update assumption op ID now that we have the real one
	if cmdName == "read" && symbol != "" {
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

// extractFileSymbol parses file and optional symbol from command args.
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
	case "refs":
		if _, hasImpact := flags["impact"]; hasImpact {
			return "refs_impact", "impact_checked"
		}
		return "refs", "refs_checked"
	case "rename":
		return "rename", "renamed"
	case "map":
		return "map", "map_viewed"
	default:
		return cmd, cmd
	}
}

