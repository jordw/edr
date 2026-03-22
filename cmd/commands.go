package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"

	"github.com/jordw/edr/internal/cmdspec"
	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/hints"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/session"
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
	rootCmd.AddCommand(verifyCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(sessionCmd)
	rootCmd.AddCommand(nextCmd)
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
	flags := extractFlags(cmd)
	if err := resolveAtFiles(flags); err != nil {
		return err
	}

	db, err := openDBStrict(cmd)
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
		emitStandaloneHints(sess, cmdName, flags, env)
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

	// Emit contextual hints to stderr
	emitStandaloneHints(sess, cmdName, flags, env)

	output.PrintEnvelope(env)
	return nil
}

// dispatchCmdWithStdin is like dispatchCmd but reads stdin into a flag first.
func dispatchCmdWithStdin(cmd *cobra.Command, cmdName string, args []string, stdinKey string) error {
	flags := extractFlags(cmd)
	if err := resolveAtFiles(flags); err != nil {
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

	db, err := openDBStrict(cmd)
	if err != nil {
		return err
	}
	defer db.Close()

	edrDir := db.EdrDir()
	sess, saveSess := session.LoadSession(edrDir)
	defer saveSess()

	injectSessionHash(sess, cmdName, args, flags)

	env := output.NewEnvelope(cmdName)
	opID := cmdName[:1] + "0"

	result, err := dispatch.Dispatch(context.Background(), db, cmdName, args, flags)
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
			verifyResult, verifyErr := dispatch.Dispatch(context.Background(), db, "verify", []string{}, map[string]any{
				"files": []string{args[0]},
			})
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

func init() { cmdspec.RegisterFlags(readCmd.Flags(), "read") }

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
	Use:   "edit <file[:symbol]>",
	Short: ToolDesc["edit"],
	Args: func(cmd *cobra.Command, args []string) error {
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

var renameCmd = &cobra.Command{
	Use:   "rename <old-name> <new-name>",
	Short: ToolDesc["rename"],
	Args:  cobra.ExactArgs(2),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "rename", args) },
}

func init() { cmdspec.RegisterFlags(renameCmd.Flags(), "rename") }

func init() {
	cmdspec.RegisterFlags(initCmd.Flags(), "reindex")
	initCmd.Flags().String("cpuprofile", "", "Write CPU profile to file")
	initCmd.Flags().MarkHidden("cpuprofile")
}

var initCmd = &cobra.Command{
	Use:     "reindex",
	Aliases: []string{"init"},
	Short:   ToolDesc["reindex"],
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
		return dispatchCmdWithIndex(cmd, "reindex", args)
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

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage sessions",
}

var sessionNewCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a new session and print its ID",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		edrDir := filepath.Join(root, ".edr")
		if err := os.MkdirAll(filepath.Join(edrDir, "sessions"), 0755); err != nil {
			return err
		}
		id := session.GenerateID()
		sess := session.New()
		path := filepath.Join(edrDir, "sessions", id+".json")
		if err := sess.SaveToFile(path); err != nil {
			return err
		}
		// Write PPID mapping so subsequent calls auto-resolve this session
		session.WriteSessionMapping(filepath.Join(edrDir, "sessions"), id)

		// Plain-mode transport: JSON header with session ID.
		fmt.Printf("{\"id\":%q}\n", id)

		cleanEdrDir(edrDir)

		return nil
	},
}

func init() {
	sessionCmd.AddCommand(sessionNewCmd)
}

var nextCmd = &cobra.Command{
	Use:   "next",
	Short: "Show session status: recent ops, build state, action items",
	Args:  cobra.NoArgs,
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

		// Open DB for assumption checking (best-effort — next works without it)
		var db *index.DB
		db, _ = openDBStrictRoot(root)
		if db != nil {
			defer db.Close()
		}

		result := buildNextResult(sess, db, count)
		env := output.NewEnvelope("next")
		env.AddOp("n0", "next", result)
		env.ComputeOK()
		output.PrintEnvelope(env)
		return nil
	},
}

func init() { cmdspec.RegisterFlags(nextCmd.Flags(), "next") }

// buildNextResult constructs the result map for `edr next`.
func buildNextResult(sess *session.Session, db *index.DB, count int) map[string]any {
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
	}

	return result
}

// computeFixItems checks all tracked assumptions against current signatures.
func computeFixItems(sess *session.Session, db *index.DB) []any {
	assumptions := sess.GetAssumptions()
	if len(assumptions) == 0 {
		return nil
	}

	// Compute current signatures for all tracked symbols
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

	stale := sess.CheckAssumptions(currentSigs)
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

// emitStandaloneHints emits contextual hints for standalone (non-batch) commands.
func emitStandaloneHints(sess *session.Session, cmdName string, flags map[string]any, env *output.Envelope) {
	f := make(map[string]bool)
	for k := range flags {
		f[k] = true
	}

	op := hints.Op{
		Kind:  cmdName,
		Flags: f,
		Meta:  make(map[string]string),
	}

	ctx := hints.Context{
		Ops:       []hints.Op{op},
		IsBatch:   false,
		HasError:  !env.OK,
		SeenHints: sess.GetSeenHints(),
	}
	keys := hints.Emit(ctx)
	sess.RecordHints(keys)
}
