package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/gather"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/search"
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

		ctx := context.Background()
		files, symbols, err := index.IndexRepo(ctx, db)
		if err != nil {
			return err
		}

		output.Print(map[string]any{
			"status":  "ok",
			"files":   files,
			"symbols": symbols,
		})
		return nil
	},
}

// --- repo-map ---

var repoMapCmd = &cobra.Command{
	Use:   "repo-map",
	Short: "Show repository symbol map",
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()
		repoMap, err := index.RepoMap(ctx, db)
		if err != nil {
			return err
		}

		files, symbols, _ := db.Stats(ctx)
		output.Print(map[string]any{
			"files":   files,
			"symbols": symbols,
			"map":     repoMap,
		})
		return nil
	},
}

// --- search (symbol search) ---

var searchCmd = &cobra.Command{
	Use:   "search <pattern>",
	Short: "Search symbols by name",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		budget, _ := cmd.Flags().GetInt("budget")

		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()
		matches, err := search.SearchSymbol(ctx, db, args[0], budget)
		if err != nil {
			return err
		}
		output.Print(matches)
		return nil
	},
}

func init() {
	searchCmd.Flags().Int("budget", 0, "token budget (0 = unlimited)")
}

// --- search-text ---

var searchTextCmd = &cobra.Command{
	Use:   "search-text <pattern>",
	Short: "Search file contents for text",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		budget, _ := cmd.Flags().GetInt("budget")

		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()
		matches, err := search.SearchText(ctx, db, args[0], budget)
		if err != nil {
			return err
		}
		output.Print(matches)
		return nil
	},
}

func init() {
	searchTextCmd.Flags().Int("budget", 0, "token budget (0 = unlimited)")
}

// --- symbols ---

var symbolsCmd = &cobra.Command{
	Use:   "symbols <file>",
	Short: "List symbols in a file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()

		// Resolve to absolute path
		file := args[0]
		if file[0] != '/' {
			file = root + "/" + file
		}

		syms, err := db.GetSymbolsByFile(ctx, file)
		if err != nil {
			return err
		}

		var results []output.Symbol
		for _, s := range syms {
			results = append(results, output.Symbol{
				Type:  s.Type,
				Name:  s.Name,
				File:  s.File,
				Lines: [2]int{int(s.StartLine), int(s.EndLine)},
				Size:  int(s.EndByte-s.StartByte) / 4,
			})
		}
		output.Print(results)
		return nil
	},
}

// --- read-symbol ---

var readSymbolCmd = &cobra.Command{
	Use:   "read-symbol <file> <symbol>",
	Short: "Read a symbol's source code",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		budget, _ := cmd.Flags().GetInt("budget")

		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()
		file := args[0]
		if file[0] != '/' {
			file = root + "/" + file
		}

		sym, err := db.GetSymbol(ctx, file, args[1])
		if err != nil {
			return err
		}

		// Read source
		src, err := os.ReadFile(sym.File)
		if err != nil {
			return err
		}

		body := string(src[sym.StartByte:sym.EndByte])
		size := len(body) / 4

		// Trim if over budget
		if budget > 0 && size > budget {
			chars := budget * 4
			if chars < len(body) {
				body = body[:chars] + "\n... (trimmed to budget)"
			}
		}

		hash, _ := edit.FileHash(sym.File)
		output.Print(output.ExpandResult{
			Symbol: output.Symbol{
				Type:  sym.Type,
				Name:  sym.Name,
				File:  sym.File,
				Lines: [2]int{int(sym.StartLine), int(sym.EndLine)},
				Size:  size,
				Hash:  hash,
			},
			Body: body,
		})
		return nil
	},
}

func init() {
	readSymbolCmd.Flags().Int("budget", 0, "token budget (0 = unlimited)")
}

// --- expand ---

var expandCmd = &cobra.Command{
	Use:   "expand <file> <symbol>",
	Short: "Expand a symbol with optional callers/deps",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		showBody, _ := cmd.Flags().GetBool("body")
		showCallers, _ := cmd.Flags().GetBool("callers")

		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()
		file := args[0]
		if file[0] != '/' {
			file = root + "/" + file
		}

		sym, err := db.GetSymbol(ctx, file, args[1])
		if err != nil {
			return err
		}

		hash, _ := edit.FileHash(sym.File)
		result := output.ExpandResult{
			Symbol: output.Symbol{
				Type:  sym.Type,
				Name:  sym.Name,
				File:  sym.File,
				Lines: [2]int{int(sym.StartLine), int(sym.EndLine)},
				Size:  int(sym.EndByte-sym.StartByte) / 4,
				Hash:  hash,
			},
		}

		if showBody {
			src, err := os.ReadFile(sym.File)
			if err != nil {
				return err
			}
			result.Body = string(src[sym.StartByte:sym.EndByte])
		}

		if showCallers {
			refs, err := index.FindReferences(ctx, db, args[1])
			if err == nil {
				allSyms, _ := db.AllSymbols(ctx)
				symMap := make(map[string][]index.SymbolInfo)
				for _, s := range allSyms {
					symMap[s.File] = append(symMap[s.File], s)
				}

				seen := make(map[string]bool)
				for _, ref := range refs {
					if ref.File == file && ref.StartLine >= sym.StartLine && ref.EndLine <= sym.EndLine {
						continue
					}
					for _, s := range symMap[ref.File] {
						if ref.StartLine >= s.StartLine && ref.EndLine <= s.EndLine {
							key := s.File + ":" + s.Name
							if !seen[key] {
								seen[key] = true
								result.Callers = append(result.Callers, output.Symbol{
									Type:  s.Type,
									Name:  s.Name,
									File:  s.File,
									Lines: [2]int{int(s.StartLine), int(s.EndLine)},
									Size:  int(s.EndByte-s.StartByte) / 4,
								})
							}
						}
					}
				}
			}
		}

		output.Print(result)
		return nil
	},
}

func init() {
	expandCmd.Flags().Bool("body", false, "include symbol body")
	expandCmd.Flags().Bool("callers", false, "include callers")
	expandCmd.Flags().Bool("deps", false, "include dependencies")
}

// --- xrefs ---

var xrefsCmd = &cobra.Command{
	Use:   "xrefs <symbol>",
	Short: "Find cross-references to a symbol",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()
		refs, err := index.FindReferences(ctx, db, args[0])
		if err != nil {
			return err
		}

		var results []output.Symbol
		for _, r := range refs {
			results = append(results, output.Symbol{
				Type:  "reference",
				Name:  r.Name,
				File:  r.File,
				Lines: [2]int{int(r.StartLine), int(r.EndLine)},
			})
		}
		output.Print(results)
		return nil
	},
}

// --- replace-symbol ---

var replaceSymbolCmd = &cobra.Command{
	Use:   "replace-symbol <file> <symbol>",
	Short: "Replace a symbol's body",
	Long:  "Reads replacement code from stdin",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		expectHash, _ := cmd.Flags().GetString("expect-hash")

		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()
		file := args[0]
		if file[0] != '/' {
			file = root + "/" + file
		}

		sym, err := db.GetSymbol(ctx, file, args[1])
		if err != nil {
			return err
		}

		// Read replacement from stdin
		replacement, err := readStdin()
		if err != nil {
			return fmt.Errorf("reading replacement from stdin: %w", err)
		}

		err = edit.ReplaceSpan(sym.File, sym.StartByte, sym.EndByte, replacement, expectHash)
		if err != nil {
			output.Print(output.EditResult{OK: false, File: sym.File, Message: err.Error()})
			return nil
		}

		output.Print(output.EditResult{OK: true, File: sym.File, Message: fmt.Sprintf("replaced symbol %s", args[1])})
		return nil
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
		root := getRoot(cmd)
		expectHash, _ := cmd.Flags().GetString("expect-hash")

		file := args[0]
		if file[0] != '/' {
			file = root + "/" + file
		}

		var startByte, endByte uint32
		fmt.Sscanf(args[1], "%d", &startByte)
		fmt.Sscanf(args[2], "%d", &endByte)

		replacement, err := readStdin()
		if err != nil {
			return fmt.Errorf("reading replacement from stdin: %w", err)
		}

		err = edit.ReplaceSpan(file, startByte, endByte, replacement, expectHash)
		if err != nil {
			output.Print(output.EditResult{OK: false, File: file, Message: err.Error()})
			return nil
		}

		output.Print(output.EditResult{OK: true, File: file, Message: "span replaced"})
		return nil
	},
}

func init() {
	replaceSpanCmd.Flags().String("expect-hash", "", "expected file hash for safety")
}

// --- gather ---

var gatherCmd = &cobra.Command{
	Use:   "gather <file> <symbol>",
	Short: "Build minimal context bundle for a symbol",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		budget, _ := cmd.Flags().GetInt("budget")

		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()

		var result *output.GatherResult
		if len(args) == 2 {
			file := args[0]
			if file[0] != '/' {
				file = root + "/" + file
			}
			result, err = gather.Gather(ctx, db, file, args[1], budget)
		} else {
			result, err = gather.GatherBySearch(ctx, db, args[0], budget)
		}
		if err != nil {
			return err
		}

		output.Print(result)
		return nil
	},
}

func init() {
	gatherCmd.Flags().Int("budget", 1500, "token budget for context")
}

// --- helpers ---

func readStdin() (string, error) {
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return "", fmt.Errorf("no input on stdin (pipe replacement code)")
	}

	data, err := os.ReadFile("/dev/stdin")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
