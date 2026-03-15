package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/pprof"

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
	rootCmd.AddCommand(exploreCmd)
	rootCmd.AddCommand(refsCmd)
	rootCmd.AddCommand(renameCmd)
	rootCmd.AddCommand(findCmd)
	rootCmd.AddCommand(verifyCmd)
	rootCmd.AddCommand(editPlanCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(setupCmd)
}

// dispatchCmd is the common pattern: open DB, dispatch, print result.
// Loads a file-backed session when EDR_SESSION is set.
func dispatchCmd(cmd *cobra.Command, cmdName string, args []string) error {
	flags := extractFlags(cmd)

	db, err := openAndEnsureIndex(cmd)
	if err != nil {
		return err
	}
	defer db.Close()

	edrDir := db.EdrDir()
	sess, saveSess := session.LoadSession(edrDir)
	defer saveSess()

	result, err := dispatch.Dispatch(context.Background(), db, cmdName, args, flags)
	if err != nil {
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

	// Apply session post-processing (delta reads, body dedup)
	data, marshalErr := json.Marshal(result)
	if marshalErr == nil {
		processed := sess.PostProcess(cmdName, args, flags, result, string(data))
		if processed != string(data) {
			output.Print(json.RawMessage(processed))
			return nil
		}
	}

	output.Print(result)
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

	db, err := openAndEnsureIndex(cmd)
	if err != nil {
		return err
	}
	defer db.Close()

	edrDir := db.EdrDir()
	sess, saveSess := session.LoadSession(edrDir)
	defer saveSess()

	result, err := dispatch.Dispatch(context.Background(), db, cmdName, args, flags)
	if err != nil {
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

	// Apply session post-processing
	data, marshalErr := json.Marshal(result)
	if marshalErr == nil {
		processed := sess.PostProcess(cmdName, args, flags, result, string(data))
		if processed != string(data) {
			output.Print(json.RawMessage(processed))
			return nil
		}
	}

	output.Print(result)
	return nil
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
	Long:  ToolDesc["plan"] + "\n\nBatch equivalent: use edits array with optional verify.",
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

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

		result, err := dispatch.Dispatch(context.Background(), db, "edit-plan", args, flags)
		if err != nil {
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
		output.Print(result)
		return nil
	},
}

func init() { cmdspec.RegisterFlags(editPlanCmd.Flags(), "edit-plan") }

var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: ToolDesc["verify"],
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "verify", args) },
}

func init() { cmdspec.RegisterFlags(verifyCmd.Flags(), "verify") }
