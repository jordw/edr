package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jordw/edr/internal/dispatch"
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
	rootCmd.AddCommand(exploreCmd)
	rootCmd.AddCommand(refsCmd)
	rootCmd.AddCommand(renameCmd)
	rootCmd.AddCommand(findCmd)
	rootCmd.AddCommand(verifyCmd)
	rootCmd.AddCommand(editPlanCmd)
	rootCmd.AddCommand(initCmd)
}

// dispatchWithSession runs a command through the session post-processing pipeline.
func dispatchWithSession(db *index.DB, sess *session.Session, cmdName string, args []string, flags map[string]any) error {
	if session.EditCommands[cmdName] || cmdName == "init" {
		sess.InvalidateForEdit(cmdName, args)
	}

	result, err := dispatch.Dispatch(context.Background(), db, cmdName, args, flags)
	if err != nil {
		return err
	}

	data, _ := json.Marshal(result)
	text := string(data)

	// Apply session post-processing (slim edits, delta reads, body stripping)
	text = sess.PostProcess(cmdName, args, flags, result, text)

	// Working-set dedup for read commands
	if session.ReadCommands[cmdName] {
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

	sess := session.New()
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

	sess := session.New()
	flags := extractFlags(cmd)
	// If the content flag was provided on CLI, skip stdin
	if v, ok := flags[stdinKey]; !ok || v == nil || v == "" {
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

func init() {
	readCmd.Flags().Int("budget", 0, P("budget"))
	readCmd.Flags().Bool("symbols", false, P("symbols"))
	readCmd.Flags().Bool("signatures", false, P("signatures"))
	readCmd.Flags().Int("depth", 0, P("depth"))
	readCmd.Flags().Bool("full", false, P("full"))
}

var writeCmd = &cobra.Command{
	Use:   "write <file>",
	Short: ToolDesc["write"],
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return dispatchCmdWithStdin(cmd, "write", args, "content")
	},
}

func init() {
	writeCmd.Flags().Bool("mkdir", false, P("mkdir"))
	writeCmd.Flags().Bool("append", false, P("append"))
	writeCmd.Flags().String("after", "", P("after"))
	writeCmd.Flags().String("inside", "", P("inside"))
}

var editCmd = &cobra.Command{
	Use:   "edit <file> [symbol]",
	Short: ToolDesc["edit"],
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return dispatchCmdWithStdin(cmd, "edit", args, "new_text")
	},
}

func init() {
	editCmd.Flags().Int("start_line", 0, P("start_line"))
	editCmd.Flags().Int("end_line", 0, P("end_line"))
	editCmd.Flags().String("old_text", "", P("old_text"))
	editCmd.Flags().String("new_text", "", P("new_text"))
	editCmd.Flags().Bool("regex", false, P("regex"))
	editCmd.Flags().Bool("all", false, P("all"))
	editCmd.Flags().Bool("dry-run", false, P("dry_run"))
	editCmd.Flags().String("move", "", "Symbol to move")
	editCmd.Flags().String("after", "", "Place after this symbol (use with --move)")
	editCmd.Flags().String("before", "", "Place before this symbol (use with --move)")
}

var mapCmd = &cobra.Command{
	Use:   "map [file]",
	Short: ToolDesc["map"],
	Args:  cobra.MaximumNArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "map", args) },
}

func init() {
	mapCmd.Flags().Int("budget", 0, P("budget"))
	mapCmd.Flags().String("dir", "", P("dir"))
	mapCmd.Flags().String("glob", "", P("glob"))
	mapCmd.Flags().String("type", "", P("type"))
	mapCmd.Flags().String("grep", "", P("grep"))
	mapCmd.Flags().Bool("locals", false, P("locals"))
}

var searchCmd = &cobra.Command{
	Use:   "search <pattern>",
	Short: ToolDesc["search"],
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "search", args) },
}

func init() {
	searchCmd.Flags().Int("budget", 0, P("budget"))
	searchCmd.Flags().Bool("body", false, P("body"))
	searchCmd.Flags().Bool("text", false, P("text"))
	searchCmd.Flags().Bool("regex", false, P("regex"))
	searchCmd.Flags().StringSlice("include", nil, P("include"))
	searchCmd.Flags().StringSlice("exclude", nil, P("exclude"))
	searchCmd.Flags().Int("context", 0, P("context"))
}

var exploreCmd = &cobra.Command{
	Use:   "explore [file] <symbol>",
	Short: ToolDesc["explore"],
	Args:  cobra.RangeArgs(1, 2),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "explore", args) },
}

func init() {
	exploreCmd.Flags().Bool("body", false, P("body"))
	exploreCmd.Flags().Bool("callers", false, P("callers"))
	exploreCmd.Flags().Bool("deps", false, P("deps"))
	exploreCmd.Flags().Bool("gather", false, P("gather"))
	exploreCmd.Flags().Bool("signatures", false, P("signatures"))
	exploreCmd.Flags().Int("budget", 0, P("budget"))
}

var refsCmd = &cobra.Command{
	Use:   "refs [file] <symbol>",
	Short: ToolDesc["refs"],
	Args:  cobra.RangeArgs(1, 2),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "refs", args) },
}

func init() {
	refsCmd.Flags().Bool("impact", false, P("impact"))
	refsCmd.Flags().String("chain", "", P("chain"))
	refsCmd.Flags().Int("depth", 3, P("depth"))
}

var renameCmd = &cobra.Command{
	Use:   "rename <old-name> <new-name>",
	Short: ToolDesc["rename"],
	Args:  cobra.ExactArgs(2),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "rename", args) },
}

func init() {
	renameCmd.Flags().Bool("dry-run", false, P("dry_run"))
	renameCmd.Flags().String("scope", "", P("scope"))
}

var findCmd = &cobra.Command{
	Use:   "find <pattern>",
	Short: ToolDesc["find"],
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "find", args) },
}

func init() {
	findCmd.Flags().String("dir", "", P("dir"))
	findCmd.Flags().Int("budget", 0, P("budget"))
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: ToolDesc["init"],
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "init", args) },
}

var editPlanCmd = &cobra.Command{
	Use:   "edit-plan",
	Short: ToolDesc["plan"],
	Long:  ToolDesc["plan"] + "\n\nMCP equivalent: edr_do with edits array and optional verify.",
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		sess := session.New()
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

func init() {
	editPlanCmd.Flags().Bool("dry-run", false, P("dry_run"))
	editPlanCmd.Flags().String("edits", "", P("edits"))
}

var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: ToolDesc["verify"],
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "verify", args) },
}

func init() {
	verifyCmd.Flags().String("command", "", P("command"))
	verifyCmd.Flags().String("level", "build", P("level"))
	verifyCmd.Flags().Int("timeout", 30, P("timeout"))
}
