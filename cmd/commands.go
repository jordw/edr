package cmd

import (
	"context"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(repoMapCmd)
	rootCmd.AddCommand(searchCmd)
	rootCmd.AddCommand(symbolsCmd)
	rootCmd.AddCommand(readSymbolCmd)
	rootCmd.AddCommand(expandCmd)
	rootCmd.AddCommand(xrefsCmd)
	rootCmd.AddCommand(replaceSymbolCmd)
	rootCmd.AddCommand(replaceSpanCmd)
	rootCmd.AddCommand(gatherCmd)
	rootCmd.AddCommand(searchTextCmd)
	rootCmd.AddCommand(replaceLinesCmd)
	rootCmd.AddCommand(readFileCmd)
	rootCmd.AddCommand(replaceTextCmd)
	rootCmd.AddCommand(writeFileCmd)
	rootCmd.AddCommand(renameSymbolCmd)
	rootCmd.AddCommand(insertAfterCmd)
	rootCmd.AddCommand(appendFileCmd)
	rootCmd.AddCommand(smartEditCmd)
	rootCmd.AddCommand(findFilesCmd)
	rootCmd.AddCommand(batchReadCmd)
	rootCmd.AddCommand(diffPreviewCmd)
	rootCmd.AddCommand(diffPreviewSpanCmd)
}

// dispatchCmd is the common pattern: open DB, extract flags, dispatch, print.
func dispatchCmd(cmd *cobra.Command, cmdName string, args []string) error {
	db, err := openAndEnsureIndex(cmd)
	if err != nil {
		return err
	}
	defer db.Close()

	flags := extractFlags(cmd)
	result, err := dispatch.Dispatch(context.Background(), db, cmdName, args, flags)
	if err != nil {
		return err
	}
	output.Print(result)
	return nil
}

// dispatchCmdWithStdin is like dispatchCmd but reads stdin into a flag first.
func dispatchCmdWithStdin(cmd *cobra.Command, cmdName string, args []string, stdinKey string) error {
	db, err := openAndEnsureIndex(cmd)
	if err != nil {
		return err
	}
	defer db.Close()

	flags := extractFlags(cmd)
	if err := readStdinToFlags(flags, stdinKey); err != nil {
		return err
	}
	result, err := dispatch.Dispatch(context.Background(), db, cmdName, args, flags)
	if err != nil {
		return err
	}
	output.Print(result)
	return nil
}

// --- init (index) ---

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Index the repository",
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		db, err := index.OpenDB(root)
		if err != nil {
			return err
		}
		defer db.Close()

		flags := extractFlags(cmd)
		result, err := dispatch.Dispatch(context.Background(), db, "init", args, flags)
		if err != nil {
			return err
		}
		output.Print(result)
		return nil
	},
}

// --- repo-map ---

var repoMapCmd = &cobra.Command{
	Use:   "repo-map",
	Short: "Show repository symbol map",
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "repo-map", args) },
}

func init() {
	repoMapCmd.Flags().Int("budget", 0, "token budget (0 = unlimited)")
}

// --- search (symbol search) ---

var searchCmd = &cobra.Command{
	Use:   "search <pattern>",
	Short: "Search symbols by name",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "search", args) },
}

func init() {
	searchCmd.Flags().Int("budget", 0, "token budget (0 = unlimited)")
	searchCmd.Flags().Bool("body", false, "include symbol body snippets")
}

// --- search-text ---

var searchTextCmd = &cobra.Command{
	Use:   "search-text <pattern>",
	Short: "Search file contents for text (searches all files, not just indexed)",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "search-text", args) },
}

func init() {
	searchTextCmd.Flags().Int("budget", 0, "token budget (0 = unlimited)")
	searchTextCmd.Flags().Bool("regex", false, "treat pattern as a Go regexp")
	searchTextCmd.Flags().StringSlice("include", nil, "glob patterns to include (e.g. *.go)")
	searchTextCmd.Flags().StringSlice("exclude", nil, "glob patterns to exclude (e.g. *_test.go)")
	searchTextCmd.Flags().Int("context", 0, "lines of context around each match")
}

// --- symbols ---

var symbolsCmd = &cobra.Command{
	Use:   "symbols <file>",
	Short: "List symbols in a file",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "symbols", args) },
}

// --- read-symbol ---

var readSymbolCmd = &cobra.Command{
	Use:   "read-symbol [file] <symbol>",
	Short: "Read a symbol's source code",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "read-symbol", args) },
}

func init() {
	readSymbolCmd.Flags().Int("budget", 0, "token budget (0 = unlimited)")
}

// --- expand ---

var expandCmd = &cobra.Command{
	Use:   "expand [file] <symbol>",
	Short: "Expand a symbol with optional callers/deps",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "expand", args) },
}

func init() {
	expandCmd.Flags().Bool("body", false, "include symbol body")
	expandCmd.Flags().Bool("callers", false, "include callers")
	expandCmd.Flags().Bool("deps", false, "include dependencies")
	expandCmd.Flags().Int("budget", 0, "token budget for body (0 = unlimited)")
}

// --- xrefs ---

var xrefsCmd = &cobra.Command{
	Use:   "xrefs <symbol>",
	Short: "Find cross-references to a symbol",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "xrefs", args) },
}

// --- replace-symbol ---

var replaceSymbolCmd = &cobra.Command{
	Use:   "replace-symbol [file] <symbol>",
	Short: "Replace a symbol's body",
	Long:  "Reads replacement code from stdin",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return dispatchCmdWithStdin(cmd, "replace-symbol", args, "replacement")
	},
}

func init() {
	replaceSymbolCmd.Flags().String("expect-hash", "", "expected file hash for safety")
}

// --- replace-span ---

var replaceSpanCmd = &cobra.Command{
	Use:   "replace-span <file> <start-byte> <end-byte>",
	Short: "Replace a byte range in a file",
	Long:  "Reads replacement code from stdin",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		return dispatchCmdWithStdin(cmd, "replace-span", args, "replacement")
	},
}

func init() {
	replaceSpanCmd.Flags().String("expect-hash", "", "expected file hash for safety")
}

// --- gather ---

var gatherCmd = &cobra.Command{
	Use:   "gather [file] <symbol>",
	Short: "Build minimal context bundle for a symbol",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "gather", args) },
}

func init() {
	gatherCmd.Flags().Int("budget", 1500, "token budget for context")
	gatherCmd.Flags().Bool("body", false, "include source bodies for target, callers, and tests")
}

// --- replace-lines ---

var replaceLinesCmd = &cobra.Command{
	Use:   "replace-lines <file> <start-line> <end-line>",
	Short: "Replace a line range in a file",
	Long:  "Reads replacement code from stdin. Lines are 1-indexed and inclusive.",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		return dispatchCmdWithStdin(cmd, "replace-lines", args, "replacement")
	},
}

func init() {
	replaceLinesCmd.Flags().String("expect-hash", "", "expected file hash for safety")
}

// --- read-file ---

var readFileCmd = &cobra.Command{
	Use:   "read-file <file> [start-line] [end-line]",
	Short: "Read a file or line range (works on any file type)",
	Args:  cobra.RangeArgs(1, 3),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "read-file", args) },
}

func init() {
	readFileCmd.Flags().Int("budget", 0, "token budget (0 = unlimited)")
	readFileCmd.Flags().Bool("symbols", false, "include symbol list for code files")
}

// --- replace-text ---

var replaceTextCmd = &cobra.Command{
	Use:   "replace-text <file> <old-text> <new-text>",
	Short: "Find and replace text in any file",
	Long:  "Replaces the first occurrence of old-text with new-text. Use --all to replace all occurrences.",
	Args:  cobra.ExactArgs(3),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "replace-text", args) },
}

func init() {
	replaceTextCmd.Flags().String("expect-hash", "", "expected file hash for safety")
	replaceTextCmd.Flags().Bool("all", false, "replace all occurrences")
	replaceTextCmd.Flags().Bool("regex", false, "treat old-text as a Go regexp")
}

// --- write-file ---

var writeFileCmd = &cobra.Command{
	Use:   "write-file <file>",
	Short: "Create or overwrite a file (reads content from stdin)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return dispatchCmdWithStdin(cmd, "write-file", args, "content")
	},
}

func init() {
	writeFileCmd.Flags().Bool("mkdir", false, "create parent directories if needed")
}

// --- rename-symbol ---

var renameSymbolCmd = &cobra.Command{
	Use:   "rename-symbol <old-name> <new-name>",
	Short: "Rename a symbol across all files",
	Args:  cobra.ExactArgs(2),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "rename-symbol", args) },
}

func init() {
	renameSymbolCmd.Flags().Bool("dry-run", false, "preview what would change without applying")
}

// --- insert-after ---

var insertAfterCmd = &cobra.Command{
	Use:   "insert-after [file] <symbol>",
	Short: "Insert code after a symbol (reads from stdin)",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return dispatchCmdWithStdin(cmd, "insert-after", args, "content")
	},
}

// --- append-file ---

var appendFileCmd = &cobra.Command{
	Use:   "append-file <file>",
	Short: "Append code to end of a file (reads from stdin)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return dispatchCmdWithStdin(cmd, "append-file", args, "content")
	},
}

// --- smart-edit ---

var smartEditCmd = &cobra.Command{
	Use:   "smart-edit [file] <symbol>",
	Short: "Read, diff-preview, and replace a symbol in one step (reads replacement from stdin)",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return dispatchCmdWithStdin(cmd, "smart-edit", args, "replacement")
	},
}

// --- diff-preview ---

var diffPreviewCmd = &cobra.Command{
	Use:   "diff-preview [file] <symbol>",
	Short: "Preview an edit as a unified diff",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return dispatchCmdWithStdin(cmd, "diff-preview", args, "replacement")
	},
}

// --- diff-preview-span ---

var diffPreviewSpanCmd = &cobra.Command{
	Use:   "diff-preview-span <file> <start-byte> <end-byte>",
	Short: "Preview a span edit as a unified diff",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		return dispatchCmdWithStdin(cmd, "diff-preview-span", args, "replacement")
	},
}

// --- find-files ---

var findFilesCmd = &cobra.Command{
	Use:   "find-files <pattern>",
	Short: "Find files by glob pattern (supports **)",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "find-files", args) },
}

func init() {
	findFilesCmd.Flags().String("dir", "", "scope search to directory")
	findFilesCmd.Flags().Int("budget", 0, "limit results by total file size in tokens")
}

// --- batch-read ---

var batchReadCmd = &cobra.Command{
	Use:   "batch-read <file-or-file:symbol> ...",
	Short: "Read multiple files/symbols in one call",
	Args:  cobra.MinimumNArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "batch-read", args) },
}

func init() {
	batchReadCmd.Flags().Int("budget", 0, "token budget (0 = unlimited)")
	batchReadCmd.Flags().Bool("symbols", false, "include symbol lists")
}
