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
	"time"

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
		output.PrintEnvelope(env)
		return silentError{code: 1}
	}

	env.AddOp(opID, cmdName, result)
	env.ComputeOK()
	output.PrintEnvelope(env)
	if !env.OK {
		return silentError{code: 1}
	}
	return nil
}

// dispatchCmd is the common pattern: open DB, dispatch, wrap in envelope, print.
// Loads a file-backed session when EDR_SESSION is set.
func dispatchCmd(cmd *cobra.Command, cmdName string, args []string) error {
	flags := extractFlags(cmd)

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
		output.PrintEnvelope(env)
		return silentError{code: 1}
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

	// Emit contextual hints to stderr
	emitStandaloneHints(sess, cmdName, flags, env)

	output.PrintEnvelope(env)
	if !env.OK {
		return silentError{code: 1}
	}
	return nil
}

// dispatchCmdWithStdin is like dispatchCmd but reads stdin into a flag first.
func dispatchCmdWithStdin(cmd *cobra.Command, cmdName string, args []string, stdinKey string) error {
	flags := extractFlags(cmd)

	// If any content-equivalent flag was provided on CLI, skip stdin.
	hasContent := false
	for _, key := range []string{stdinKey, "content", "new_text", "body"} {
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

	env := output.NewEnvelope(cmdName)
	opID := cmdName[:1] + "0"

	result, err := dispatch.Dispatch(context.Background(), db, cmdName, args, flags)
	if err != nil {
		addDispatchFailedOp(env, opID, cmdName, err)
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
	}

	env.ComputeOK()
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
	Use:   "edit <file> [symbol]",
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
	Use:   "refs [file] <symbol>",
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

		env := output.NewEnvelope("verify")

		result, dispErr := dispatch.DispatchVerify(context.Background(), absRoot, args, flags)
		if dispErr != nil {
			env.SetVerify(map[string]any{"status": "failed", "error": dispErr.Error()})
		} else {
			env.SetVerify(result)
		}
		env.ComputeOK()
		output.PrintEnvelope(env)
		if !env.OK {
			return silentError{code: 1}
		}
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
		ppid := os.Getppid()
		ppidPath := filepath.Join(edrDir, "sessions", fmt.Sprintf("ppid_%d", ppid))
		os.WriteFile(ppidPath, []byte(id), 0644)

		fmt.Println(id)

		// Clean up session files older than 7 days
		sessDir := filepath.Join(edrDir, "sessions")
		entries, _ := os.ReadDir(sessDir)
		cutoff := time.Now().Add(-7 * 24 * time.Hour)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			os.Remove(filepath.Join(sessDir, e.Name()))
		}

		return nil
	},
}

func init() {
	sessionCmd.AddCommand(sessionNewCmd)
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
	case strings.Contains(msg, "hash mismatch"):
		return "hash_mismatch"
	default:
		return "command_error"
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
