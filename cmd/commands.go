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
	Short: "Read files, symbols, or batches",
	Long: `Read command:
  read file.go                     Read entire file
  read file.go 10 50               Read line range
  read file.go parseConfig         Read a symbol
  read file.go:parseConfig         Read a symbol (colon syntax)
  read f1.go f2.go f3.go:sym       Batch read multiple files/symbols`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "read", args) },
}

func init() {
	readCmd.Flags().Int("budget", 0, "token budget (0 = unlimited)")
	readCmd.Flags().Bool("symbols", false, "include symbol list for code files")
	readCmd.Flags().Bool("signatures", false, "for containers: return method signatures only (no bodies)")
	readCmd.Flags().Int("depth", 0, "progressive disclosure depth (1=signatures, 2=bodies with blocks collapsed, 3+=more)")
	readCmd.Flags().Bool("full", false, "force full content (skip delta optimization)")
}

var writeCmd = &cobra.Command{
	Use:   "write <file>",
	Short: "Create, overwrite, append, or insert into containers",
	Long: `Write command (content from stdin):
  write file.go                          Create or overwrite
  write file.go --append                 Append to file
  write file.go --after symbol           Insert after a symbol
  write file.go --inside MyClass         Insert inside a class/struct/impl
  write file.go --mkdir                  Create parent directories`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return dispatchCmdWithStdin(cmd, "write", args, "content")
	},
}

func init() {
	writeCmd.Flags().Bool("mkdir", false, "create parent directories if needed")
	writeCmd.Flags().Bool("append", false, "append to existing file instead of overwriting")
	writeCmd.Flags().String("after", "", "insert content after this symbol")
	writeCmd.Flags().String("inside", "", "insert content inside a container symbol (class/struct/impl)")
}

var editCmd = &cobra.Command{
	Use:   "edit <file> [symbol]",
	Short: "Edit by text match, symbol, or line range",
	Long: `Edit command (new_text from stdin):
  edit file.go --old_text "old code"   Find and replace
  edit file.go --old_text "x" --all    Replace all occurrences
  edit file.go parseConfig             Replace entire symbol body
  edit file.go --start_line 10 --end_line 20   Replace line range`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return dispatchCmdWithStdin(cmd, "edit", args, "new_text")
	},
}

func init() {
	editCmd.Flags().Int("start_line", 0, "start line for line-range mode")
	editCmd.Flags().Int("end_line", 0, "end line for line-range mode")
	editCmd.Flags().String("old_text", "", "text to find and replace")
	editCmd.Flags().String("new_text", "", "replacement text (alternative to stdin)")
	editCmd.Flags().Bool("regex", false, "treat --old_text as regex")
	editCmd.Flags().Bool("all", false, "replace all occurrences (with --old_text)")
	editCmd.Flags().Bool("dry-run", false, "preview changes as a diff without applying")
}

var mapCmd = &cobra.Command{
	Use:   "map [file]",
	Short: "Symbol map of repo or file",
	Long: `Map command:
  map                              Repo-wide symbol map
  map file.go                      Symbols in a specific file
  map --dir internal/ --type func  Filtered repo map`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "map", args) },
}

func init() {
	mapCmd.Flags().Int("budget", 0, "token budget (0 = unlimited)")
	mapCmd.Flags().String("dir", "", "only show files under this directory")
	mapCmd.Flags().String("glob", "", "only show files matching this glob pattern")
	mapCmd.Flags().String("type", "", "only show symbols of this type (function, type, variable)")
	mapCmd.Flags().String("grep", "", "only show symbols whose name contains this string")
}

var searchCmd = &cobra.Command{
	Use:   "search <pattern>",
	Short: "Search symbols or text",
	Long: `Search command:
  search parseConfig               Symbol search
  search parseConfig --body        Symbol search with body snippets
  search "TODO" --text             Text search across all files
  search "func.*" --regex          Text search with regex
  search "err" --include "*.go"    Text search filtered by glob`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "search", args) },
}

func init() {
	searchCmd.Flags().Int("budget", 0, "token budget (0 = unlimited)")
	searchCmd.Flags().Bool("body", false, "include symbol body snippets")
	searchCmd.Flags().Bool("text", false, "force text search mode")
	searchCmd.Flags().Bool("regex", false, "treat pattern as regex (implies --text)")
	searchCmd.Flags().StringSlice("include", nil, "glob patterns to include (implies --text)")
	searchCmd.Flags().StringSlice("exclude", nil, "glob patterns to exclude (implies --text)")
	searchCmd.Flags().Int("context", 0, "lines of context around matches (implies --text)")
}

var exploreCmd = &cobra.Command{
	Use:   "explore [file] <symbol>",
	Short: "Explore a symbol: body, callers, deps, or full context gather",
	Long: `Explore command:
  explore sym --body --callers     Expand with body and callers
  explore sym --gather             Full context bundle (callers + tests)
  explore sym --body --deps        Show body and dependencies`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "explore", args) },
}

func init() {
	exploreCmd.Flags().Bool("body", false, "include symbol body")
	exploreCmd.Flags().Bool("callers", false, "include callers")
	exploreCmd.Flags().Bool("deps", false, "include dependencies")
	exploreCmd.Flags().Bool("gather", false, "gather mode: callers + tests within budget")
	exploreCmd.Flags().Bool("signatures", false, "include extracted signatures")
	exploreCmd.Flags().Int("budget", 0, "token budget (0 = unlimited)")
}

var refsCmd = &cobra.Command{
	Use:   "refs [file] <symbol>",
	Short: "Find references, impact, or call chains",
	Long: `Refs command:
  refs parseConfig                 Find all references
  refs parseConfig --impact        Transitive impact analysis
  refs parseConfig --chain main    Find call path to another symbol`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "refs", args) },
}

func init() {
	refsCmd.Flags().Bool("impact", false, "transitive impact analysis (BFS through callers)")
	refsCmd.Flags().String("chain", "", "find call chain from this symbol to target")
	refsCmd.Flags().Int("depth", 3, "max depth for --impact or --chain")
}

var renameCmd = &cobra.Command{
	Use:   "rename <old-name> <new-name>",
	Short: "Rename a symbol across all files",
	Args:  cobra.ExactArgs(2),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "rename", args) },
}

func init() {
	renameCmd.Flags().Bool("dry-run", false, "preview what would change without applying")
	renameCmd.Flags().String("scope", "", "glob pattern to limit rename scope")
}

var findCmd = &cobra.Command{
	Use:   "find <pattern>",
	Short: "Find files by glob pattern (supports **)",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "find", args) },
}

func init() {
	findCmd.Flags().String("dir", "", "scope search to directory")
	findCmd.Flags().Int("budget", 0, "limit results by total file size in tokens")
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Index the repository",
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "init", args) },
}

var editPlanCmd = &cobra.Command{
	Use:   "edit-plan",
	Short: "Apply multiple edits atomically (edits via --edits JSON or stdin)",
	Long:  "Each edit can be symbol-based, line-based, or text-based. Supports --dry-run.\nPass edits as --edits '<JSON array>' or pipe JSON via stdin.",
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
	editPlanCmd.Flags().Bool("dry-run", false, "preview what would change without applying")
	editPlanCmd.Flags().String("edits", "", "JSON array of edits")
}

var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Run a verification command (build/typecheck) and return structured result",
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "verify", args) },
}

func init() {
	verifyCmd.Flags().String("command", "", "shell command to run (auto-detects if empty)")
	verifyCmd.Flags().Int("timeout", 30, "timeout in seconds")
}
