package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"

	"github.com/jordw/edr/internal/cmdspec"
	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/session"
	"github.com/jordw/edr/internal/trace"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(doCmd)
	rootCmd.AddCommand(readCmd)
	rootCmd.AddCommand(writeCmd)
	rootCmd.AddCommand(editCmd)
	rootCmd.AddCommand(mapCmd)
	rootCmd.AddCommand(searchCmd)
	rootCmd.AddCommand(exploreCmd)
	rootCmd.AddCommand(refsCmd)
	rootCmd.AddCommand(renameCmd)
	rootCmd.AddCommand(findCmd)
	rootCmd.AddCommand(verifyCmd)
	rootCmd.AddCommand(editPlanCmd)
	rootCmd.AddCommand(initCmd)
}

var doCmd = &cobra.Command{
	Use:   "do [json]",
	Short: "Execute a batched operation",
	Long: `Accepts a JSON object with reads, queries, edits, writes, renames,
verify, and init — all in one call. Reads JSON from the first argument or stdin.

JSON can also be passed directly to the root command (edr '{...}').

Example:
  edr '{"reads":[{"file":"src/main.go","symbol":"Server"}]}'
  edr do '{"reads":[{"file":"src/main.go","symbol":"Server"}]}'
  echo '{"edits":[{"file":"f.go","old_text":"x","new_text":"y"}],"verify":true}' | edr`,
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		var raw json.RawMessage
		if len(args) > 0 {
			raw = json.RawMessage(args[0])
		} else {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("reading stdin: %w", err)
			}
			raw = json.RawMessage(data)
		}

		sess, err := openSession(cmd, db)
		if err != nil {
			return err
		}
		defer sess.Close()
		edrDir := filepath.Join(db.Root(), ".edr")
		tc := trace.NewCollector(edrDir, Version+"+"+BuildHash)
		defer tc.Close()
		ctx := context.Background()
		text, err := handleDo(ctx, db, sess, tc, raw)
		if err != nil {
			return err
		}

		var out any
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			fmt.Println(text)
		} else {
			output.Print(out)
		}
		return nil
	},
}

// dispatchWithSession runs a command through the session post-processing pipeline.
func dispatchWithSession(db *index.DB, sess *session.Session, cmdName string, args []string, flags map[string]any) error {
	if cmdspec.ModifiesState(cmdName) {
		sess.InvalidateForEdit(cmdName, args)
	}

	result, err := dispatch.Dispatch(context.Background(), db, cmdName, args, flags)
	if err != nil {
		// Surface structured errors as JSON instead of bare strings
		var nfErr *dispatch.NotFoundError
		if errors.As(err, &nfErr) {
			data, _ := json.Marshal(map[string]any{"ok": false, "error": nfErr})
			output.Print(json.RawMessage(data))
			return nil
		}
		if ambErr := asAmbiguousError(err); ambErr != nil {
			data, _ := json.Marshal(map[string]any{"ok": false, "error": ambErr})
			output.Print(json.RawMessage(data))
			return nil
		}
		return err
	}

	data, _ := json.Marshal(result)
	text := string(data)

	// Apply session post-processing (slim edits, delta reads, body stripping)
	text = sess.PostProcess(cmdName, args, flags, result, text)

	// Working-set dedup for read commands
	if cmdspec.IsRead(cmdName) {
		key := sess.CacheKey(cmdName, args, flags)
		if sess.Check(key, text) {
			text = fmt.Sprintf(`{"cached":true,"message":"identical to previous response for %s %s"}`, cmdName, strings.Join(args, " "))
		}
	}

	// Print the post-processed result
	var out any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		output.Print(result)
	} else {
		output.Print(out)
	}
	return nil
}

// dispatchCmd is the common pattern: open DB, dispatch, post-process.
func dispatchCmd(cmd *cobra.Command, cmdName string, args []string) error {
	db, err := openAndEnsureIndex(cmd)
	if err != nil {
		return err
	}
	defer db.Close()

	sess, err := openSession(cmd, db)
	if err != nil {
		return err
	}
	defer sess.Close()
	flags := extractFlags(cmd)
	return dispatchWithSession(db, sess, cmdName, args, flags)
}

// dispatchCmdWithStdin is like dispatchCmd but reads stdin into a flag first.
func dispatchCmdWithStdin(cmd *cobra.Command, cmdName string, args []string, stdinKey string) error {
	db, err := openAndEnsureIndex(cmd)
	if err != nil {
		return err
	}
	defer db.Close()

	sess, err := openSession(cmd, db)
	if err != nil {
		return err
	}
	defer sess.Close()
	flags := extractFlags(cmd)
	// If any content-equivalent flag was provided on CLI, skip stdin.
	// An explicitly-set empty string (e.g. --new_text '') is a valid value
	// (deletion), so we check existence in the map, not emptiness.
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
	return dispatchWithSession(db, sess, cmdName, args, flags)
}

// =====================================================================
// Commands
// =====================================================================

var readCmd = &cobra.Command{
	Use:   "read <file> [start] [end] | <file> <symbol> | <file>:<symbol> ...",
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
		// --move doesn't need stdin content; dispatch directly
		if move, _ := cmd.Flags().GetString("move"); move != "" {
			return dispatchCmd(cmd, "edit", args)
		}
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

var exploreCmd = &cobra.Command{
	Use:   "explore [file] <symbol>",
	Short: ToolDesc["explore"],
	Args:  cobra.RangeArgs(1, 2),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "explore", args) },
}

func init() { cmdspec.RegisterFlags(exploreCmd.Flags(), "explore") }

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

var findCmd = &cobra.Command{
	Use:   "find <pattern>",
	Short: ToolDesc["find"],
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "find", args) },
}

func init() {
	cmdspec.RegisterFlags(findCmd.Flags(), "find")
	cmdspec.RegisterFlags(initCmd.Flags(), "init")
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: ToolDesc["init"],
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
		return dispatchCmd(cmd, "init", args)
	},
}

var editPlanCmd = &cobra.Command{
	Use:   "edit-plan",
	Short: ToolDesc["plan"],
	Long:  ToolDesc["plan"] + "\n\nBatch equivalent: edr do with edits array and optional verify.",
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		sess, err := openSession(cmd, db)
		if err != nil {
			return err
		}
		defer sess.Close()
		flags := extractFlags(cmd)
		editsStr, _ := cmd.Flags().GetString("edits")
		if editsStr != "" {
			var edits []any
			if err := json.Unmarshal([]byte(editsStr), &edits); err != nil {
				return fmt.Errorf("edit-plan: invalid --edits JSON: %w", err)
			}
			flags["edits"] = edits
		} else if flags["edits"] == nil {
			stat, _ := os.Stdin.Stat()
			if (stat.Mode() & os.ModeCharDevice) == 0 {
				data, err := io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("edit-plan: reading stdin: %w", err)
				}
				if len(data) > 0 {
					var edits []any
					if err := json.Unmarshal(data, &edits); err != nil {
						return fmt.Errorf("edit-plan: invalid stdin JSON: %w", err)
					}
					flags["edits"] = edits
				}
			}
			if flags["edits"] == nil {
				return fmt.Errorf("edit-plan: provide edits via --edits flag or pipe JSON to stdin")
			}
		}

		return dispatchWithSession(db, sess, "edit-plan", args, flags)
	},
}

func init() { cmdspec.RegisterFlags(editPlanCmd.Flags(), "edit-plan") }

var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: ToolDesc["verify"],
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "verify", args) },
}

func init() { cmdspec.RegisterFlags(verifyCmd.Flags(), "verify") }
