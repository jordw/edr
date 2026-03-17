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

	if cmdName == "read" {
		result = liftSymbolFields(result)
	}

	env.AddOp(opID, cmdName, result)
	env.ComputeOK()
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
				env.SetVerify(map[string]any{"ok": false, "error": verifyErr.Error()})
			} else {
				env.SetVerify(verifyResult)
			}
		} else if dryRun {
			env.SetVerify(map[string]any{"skipped": "dry run"})
		}
	}

	env.ComputeOK()
	output.PrintEnvelope(env)
	if !env.OK {
		if env.IsVerifyOnlyFailure() {
			return silentError{code: 2}
		}
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
	Args:  cobra.RangeArgs(1, 2),
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
			env.SetVerify(map[string]any{"ok": false, "error": dispErr.Error()})
		} else {
			env.SetVerify(result)
		}
		env.ComputeOK()
		output.PrintEnvelope(env)
		if !env.OK {
			return silentError{code: 2}
		}
		return nil
	},
}

func init() { cmdspec.RegisterFlags(verifyCmd.Flags(), "verify") }

// liftSymbolFields promotes file and hash from the nested "symbol" sub-object
// to the top level of a read result, for envelope consistency.
func liftSymbolFields(result any) any {
	m, ok := result.(map[string]any)
	if !ok {
		// Try JSON roundtrip for struct types
		data, err := json.Marshal(result)
		if err != nil {
			return result
		}
		if json.Unmarshal(data, &m) != nil {
			return result
		}
	}
	sym, ok := m["symbol"].(map[string]any)
	if !ok {
		return m
	}
	// Lift file and hash to top level if not already present
	if _, has := m["file"]; !has {
		if f, ok := sym["file"]; ok {
			m["file"] = f
		}
	}
	if _, has := m["hash"]; !has {
		if h, ok := sym["hash"]; ok {
			m["hash"] = h
		}
	}
	return m
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

	// Classify op-level errors with specific codes
	code := classifyError(err)
	env.AddFailedOpWithCode(opID, opType, code, err.Error())
}

// classifyError maps dispatch errors to structured error codes.
func classifyError(err error) string {
	var nfe *dispatch.NotFoundError
	if errors.As(err, &nfe) {
		return "not_found"
	}
	return classifyErrorMsg(err.Error())
}

// classifyErrorMsg classifies an error message string into a structured code.
func classifyErrorMsg(msg string) string {
	switch {
	case strings.Contains(msg, "not found"):
		return "not_found"
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
