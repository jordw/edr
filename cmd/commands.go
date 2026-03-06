package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"regexp"

	"github.com/jordw/edr/internal/dispatch"
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
		filesChanged, symbolsChanged, err := index.IndexRepo(ctx, db)
		if err != nil {
			return err
		}
		totalFiles, totalSymbols, _ := db.Stats(ctx)

		output.Print(map[string]any{
			"status":          "ok",
			"files_changed":   filesChanged,
			"symbols_changed": symbolsChanged,
			"total_files":     totalFiles,
			"total_symbols":   totalSymbols,
		})
		return nil
	},
}

// --- repo-map ---

var repoMapCmd = &cobra.Command{
	Use:   "repo-map",
	Short: "Show repository symbol map",
	RunE: func(cmd *cobra.Command, args []string) error {
		budget, _ := cmd.Flags().GetInt("budget")

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

		if budget > 0 {
			size := len(repoMap) / 4
			if size > budget {
				chars := budget * 4
				if chars < len(repoMap) {
					repoMap = repoMap[:chars] + "\n... (trimmed to budget)"
				}
			}
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

func init() {
	repoMapCmd.Flags().Int("budget", 0, "token budget (0 = unlimited)")
}

// --- search (symbol search) ---

var searchCmd = &cobra.Command{
	Use:   "search <pattern>",
	Short: "Search symbols by name",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		budget, _ := cmd.Flags().GetInt("budget")
		showBody, _ := cmd.Flags().GetBool("body")

		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()
		matches, err := search.SearchSymbol(ctx, db, args[0], budget, showBody)
		if err != nil {
			return err
		}
		output.Print(matches)
		return nil
	},
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
	RunE: func(cmd *cobra.Command, args []string) error {
		budget, _ := cmd.Flags().GetInt("budget")
		useRegex, _ := cmd.Flags().GetBool("regex")
		include, _ := cmd.Flags().GetStringSlice("include")
		exclude, _ := cmd.Flags().GetStringSlice("exclude")

		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		var opts []search.SearchTextOption
		if len(include) > 0 {
			opts = append(opts, search.WithInclude(include...))
		}
		if len(exclude) > 0 {
			opts = append(opts, search.WithExclude(exclude...))
		}
		ctxLines, _ := cmd.Flags().GetInt("context")
		if ctxLines > 0 {
			opts = append(opts, search.WithContext(ctxLines))
		}

		ctx := context.Background()
		matches, err := search.SearchText(ctx, db, args[0], budget, useRegex, opts...)
		if err != nil {
			return err
		}
		output.Print(matches)
		return nil
	},
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
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()

		file, err := db.ResolvePath(args[0])
		if err != nil {
			return err
		}

		syms, err := db.GetSymbolsByFile(ctx, file)
		if err != nil {
			return err
		}

		nameCounts := make(map[string]int)
		for _, s := range syms {
			nameCounts[s.Name]++
		}
		var results []output.Symbol
		for _, s := range syms {
			sym := output.Symbol{
				Type:  s.Type,
				Name:  s.Name,
				File:  output.Rel(s.File),
				Lines: [2]int{int(s.StartLine), int(s.EndLine)},
				Size:  int(s.EndByte-s.StartByte) / 4,
			}
			if nameCounts[s.Name] > 1 {
				sym.Qualifier = fmt.Sprintf("line %d", s.StartLine)
			}
			results = append(results, sym)
		}
		output.Print(results)
		return nil
	},
}

// --- read-symbol ---

var readSymbolCmd = &cobra.Command{
	Use:   "read-symbol [file] <symbol>",
	Short: "Read a symbol's source code",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		budget, _ := cmd.Flags().GetInt("budget")

		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()
		sym, err := resolveSymbol(ctx, db, args)
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
				File:  output.Rel(sym.File),
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
	Use:   "expand [file] <symbol>",
	Short: "Expand a symbol with optional callers/deps",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		showBody, _ := cmd.Flags().GetBool("body")
		showCallers, _ := cmd.Flags().GetBool("callers")
		showDeps, _ := cmd.Flags().GetBool("deps")
		budget, _ := cmd.Flags().GetInt("budget")

		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()
		sym, err := resolveSymbol(ctx, db, args)
		if err != nil {
			return err
		}

		hash, _ := edit.FileHash(sym.File)
		result := output.ExpandResult{
			Symbol: output.Symbol{
				Type:  sym.Type,
				Name:  sym.Name,
				File:  output.Rel(sym.File),
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
			body := string(src[sym.StartByte:sym.EndByte])
			if budget > 0 {
				size := len(body) / 4
				if size > budget {
					chars := budget * 4
					if chars < len(body) {
						body = body[:chars] + "\n... (trimmed to budget)"
					}
				}
			}
			result.Body = body
		}

		if showCallers {
			refs, err := index.FindReferences(ctx, db, sym.Name)
			if err == nil {
				allSyms, _ := db.AllSymbols(ctx)
				symMap := make(map[string][]index.SymbolInfo)
				for _, s := range allSyms {
					symMap[s.File] = append(symMap[s.File], s)
				}

				seen := make(map[string]bool)
				for _, ref := range refs {
					if ref.File == sym.File && ref.StartLine >= sym.StartLine && ref.EndLine <= sym.EndLine {
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
									File:  output.Rel(s.File),
									Lines: [2]int{int(s.StartLine), int(s.EndLine)},
									Size:  int(s.EndByte-s.StartByte) / 4,
								})
							}
						}
					}
				}
			}
		}

		if showDeps {
			deps, err := index.FindDeps(ctx, db, sym)
			if err == nil {
				for _, d := range deps {
					result.Deps = append(result.Deps, output.Symbol{
						Type:  d.Type,
						Name:  d.Name,
						File:  output.Rel(d.File),
						Lines: [2]int{int(d.StartLine), int(d.EndLine)},
						Size:  int(d.EndByte-d.StartByte) / 4,
					})
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
	expandCmd.Flags().Int("budget", 0, "token budget for body (0 = unlimited)")
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
				File:  output.Rel(r.File),
				Lines: [2]int{int(r.StartLine), int(r.EndLine)},
			})
		}
		output.Print(results)
		return nil
	},
}

// --- replace-symbol ---

var replaceSymbolCmd = &cobra.Command{
	Use:   "replace-symbol [file] <symbol>",
	Short: "Replace a symbol's body",
	Long:  "Reads replacement code from stdin",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		expectHash, _ := cmd.Flags().GetString("expect-hash")

		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()
		sym, err := resolveSymbol(ctx, db, args)
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
			output.Print(output.EditResult{OK: false, File: output.Rel(sym.File), Message: err.Error()})
			return nil
		}

		// Re-index the modified file
		_ = index.IndexFile(ctx, db, sym.File)

		hash, _ := edit.FileHash(sym.File)
		output.Print(output.EditResult{OK: true, File: output.Rel(sym.File), Message: fmt.Sprintf("replaced symbol %s", sym.Name), Hash: hash})
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
		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		expectHash, _ := cmd.Flags().GetString("expect-hash")

		file, err := db.ResolvePath(args[0])
		if err != nil {
			return err
		}

		var startByte, endByte uint32
		fmt.Sscanf(args[1], "%d", &startByte)
		fmt.Sscanf(args[2], "%d", &endByte)

		replacement, err := readStdin()
		if err != nil {
			return fmt.Errorf("reading replacement from stdin: %w", err)
		}

		ctx := context.Background()
		err = edit.ReplaceSpan(file, startByte, endByte, replacement, expectHash)
		if err != nil {
			output.Print(output.EditResult{OK: false, File: output.Rel(file), Message: err.Error()})
			return nil
		}

		// Re-index the modified file
		_ = index.IndexFile(ctx, db, file)

		hash, _ := edit.FileHash(file)
		output.Print(output.EditResult{OK: true, File: output.Rel(file), Message: "span replaced", Hash: hash})
		return nil
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
	RunE: func(cmd *cobra.Command, args []string) error {
		budget, _ := cmd.Flags().GetInt("budget")

		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()

		includeBody, _ := cmd.Flags().GetBool("body")

		var result *output.GatherResult
		// Try exact symbol resolution first (works with 1 or 2 args)
		sym, resolveErr := resolveSymbol(ctx, db, args)
		if resolveErr == nil {
			result, err = gather.Gather(ctx, db, sym.File, sym.Name, budget, includeBody)
		} else if len(args) == 1 {
			// Fall back to search-based gather
			result, err = gather.GatherBySearch(ctx, db, args[0], budget, includeBody)
		} else {
			return resolveErr
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
	gatherCmd.Flags().Bool("body", false, "include source bodies for target, callers, and tests")
}

// --- replace-lines ---

var replaceLinesCmd = &cobra.Command{
	Use:   "replace-lines <file> <start-line> <end-line>",
	Short: "Replace a line range in a file",
	Long:  "Reads replacement code from stdin. Lines are 1-indexed and inclusive.",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		expectHash, _ := cmd.Flags().GetString("expect-hash")

		file, err := db.ResolvePath(args[0])
		if err != nil {
			return err
		}

		var startLine, endLine int
		fmt.Sscanf(args[1], "%d", &startLine)
		fmt.Sscanf(args[2], "%d", &endLine)

		replacement, err := readStdin()
		if err != nil {
			return fmt.Errorf("reading replacement from stdin: %w", err)
		}

		ctx := context.Background()
		err = edit.ReplaceLines(file, startLine, endLine, replacement, expectHash)
		if err != nil {
			output.Print(output.EditResult{OK: false, File: output.Rel(file), Message: err.Error()})
			return nil
		}

		_ = index.IndexFile(ctx, db, file)

		hash, _ := edit.FileHash(file)
		output.Print(output.EditResult{OK: true, File: output.Rel(file), Message: fmt.Sprintf("replaced lines %d-%d", startLine, endLine), Hash: hash})
		return nil
	},
}

func init() {
	rootCmd.AddCommand(replaceLinesCmd)
	replaceLinesCmd.Flags().String("expect-hash", "", "expected file hash for safety")
}

// --- read-file ---

var readFileCmd = &cobra.Command{
	Use:   "read-file <file> [start-line] [end-line]",
	Short: "Read a file or line range (works on any file type)",
	Args:  cobra.RangeArgs(1, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		budget, _ := cmd.Flags().GetInt("budget")
		root, err := index.NormalizeRoot(getRoot(cmd))
		if err != nil {
			return err
		}
		file, err := index.ResolvePath(root, args[0])
		if err != nil {
			return err
		}

		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}

		content := string(data)
		lines := strings.SplitAfter(content, "\n")
		totalLines := len(lines)

		startLine := 1
		endLine := totalLines
		if len(args) >= 2 {
			fmt.Sscanf(args[1], "%d", &startLine)
		}
		if len(args) >= 3 {
			fmt.Sscanf(args[2], "%d", &endLine)
		}

		// Clamp
		if startLine < 1 {
			startLine = 1
		}
		if endLine > totalLines {
			endLine = totalLines
		}

		// Extract the requested lines with line numbers
		var numbered strings.Builder
		if startLine <= endLine {
			for i, line := range lines[startLine-1 : endLine] {
				fmt.Fprintf(&numbered, "%d\t%s", startLine+i, line)
			}
		}
		body := numbered.String()

		rawSize := 0
		for _, line := range lines[startLine-1 : endLine] {
			rawSize += len(line)
		}
		size := rawSize / 4
		truncated := false
		if budget > 0 && size > budget {
			chars := budget * 4
			if chars < len(body) {
				body = body[:chars] + "\n... (trimmed to budget)"
				truncated = true
			}
			size = budget
		}

		hash, _ := edit.FileHash(file)
		result := map[string]any{
			"file":        output.Rel(file),
			"lines":       [2]int{startLine, endLine},
			"total_lines": totalLines,
			"size":        size,
			"content":     body,
			"hash":        hash,
			"truncated":   truncated,
		}

		showSymbols, _ := cmd.Flags().GetBool("symbols")
		if showSymbols {
			db, dbErr := openAndEnsureIndex(cmd)
			if dbErr == nil {
				ctx := context.Background()
				syms, err := db.GetSymbolsByFile(ctx, file)
				db.Close()
				if err == nil && len(syms) > 0 {
					var symList []output.Symbol
					for _, s := range syms {
						symList = append(symList, output.Symbol{
							Type:  s.Type,
							Name:  s.Name,
							Lines: [2]int{int(s.StartLine), int(s.EndLine)},
							Size:  int(s.EndByte-s.StartByte) / 4,
						})
					}
					result["symbols"] = symList
				}
			}
		}

		output.Print(result)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(readFileCmd)
	readFileCmd.Flags().Int("budget", 0, "token budget (0 = unlimited)")
	readFileCmd.Flags().Bool("symbols", false, "include symbol list for code files")
}

// --- replace-text ---

var replaceTextCmd = &cobra.Command{
	Use:   "replace-text <file> <old-text> <new-text>",
	Short: "Find and replace text in any file",
	Long:  "Replaces the first occurrence of old-text with new-text. Use --all to replace all occurrences.",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := index.NormalizeRoot(getRoot(cmd))
		if err != nil {
			return err
		}
		expectHash, _ := cmd.Flags().GetString("expect-hash")
		replaceAll, _ := cmd.Flags().GetBool("all")
		useRegex, _ := cmd.Flags().GetBool("regex")

		file, err := index.ResolvePath(root, args[0])
		if err != nil {
			return err
		}
		oldText := args[1]
		newText := args[2]

		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}

		if expectHash != "" {
			hash, _ := edit.FileHash(file)
			if hash != expectHash {
				output.Print(output.EditResult{OK: false, File: output.Rel(file), Message: fmt.Sprintf("hash mismatch: expected %s, got %s", expectHash, hash)})
				return nil
			}
		}

		content := string(data)

		var result string
		var count int
		if useRegex {
			re, err := regexp.Compile(oldText)
			if err != nil {
				return fmt.Errorf("invalid regex: %w", err)
			}
			matches := re.FindAllStringIndex(content, -1)
			if len(matches) == 0 {
				output.Print(output.EditResult{OK: false, File: output.Rel(file), Message: "pattern not found in file"})
				return nil
			}
			if replaceAll {
				count = len(matches)
				result = re.ReplaceAllString(content, newText)
			} else {
				count = 1
				loc := matches[0]
				result = content[:loc[0]] + re.ReplaceAllString(content[loc[0]:loc[1]], newText) + content[loc[1]:]
			}
		} else {
			if !strings.Contains(content, oldText) {
				output.Print(output.EditResult{OK: false, File: output.Rel(file), Message: "old-text not found in file"})
				return nil
			}
			if replaceAll {
				count = strings.Count(content, oldText)
				result = strings.ReplaceAll(content, oldText, newText)
			} else {
				count = 1
				result = strings.Replace(content, oldText, newText, 1)
			}
		}

		info, err := os.Stat(file)
		if err != nil {
			return err
		}
		if err := os.WriteFile(file, []byte(result), info.Mode()); err != nil {
			return err
		}

		db, dbErr := openAndEnsureIndex(cmd)
		if dbErr == nil {
			ctx := context.Background()
			_ = index.IndexFile(ctx, db, file)
			db.Close()
		}

		hash, _ := edit.FileHash(file)
		output.Print(output.EditResult{OK: true, File: output.Rel(file), Message: fmt.Sprintf("replaced %d occurrence(s)", count), Hash: hash})
		return nil
	},
}

func init() {
	rootCmd.AddCommand(replaceTextCmd)
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
		root, err := index.NormalizeRoot(getRoot(cmd))
		if err != nil {
			return err
		}
		mkdir, _ := cmd.Flags().GetBool("mkdir")

		file, err := index.ResolvePath(root, args[0])
		if err != nil {
			return err
		}

		content, err := readStdin()
		if err != nil {
			return fmt.Errorf("reading content from stdin: %w", err)
		}

		if mkdir {
			dir := file[:strings.LastIndex(file, "/")]
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("mkdir: %w", err)
			}
		}

		if err := os.WriteFile(file, []byte(content), 0644); err != nil {
			return err
		}

		// Index if it's a supported language
		db, dbErr := openAndEnsureIndex(cmd)
		if dbErr == nil {
			ctx := context.Background()
			_ = index.IndexFile(ctx, db, file)
			db.Close()
		}

		hash, _ := edit.FileHash(file)
		output.Print(output.EditResult{OK: true, File: output.Rel(file), Message: fmt.Sprintf("wrote %d bytes", len(content)), Hash: hash})
		return nil
	},
}

func init() {
	rootCmd.AddCommand(writeFileCmd)
	writeFileCmd.Flags().Bool("mkdir", false, "create parent directories if needed")
}

// --- rename-symbol ---

var renameSymbolCmd = &cobra.Command{
	Use:   "rename-symbol <old-name> <new-name>",
	Short: "Rename a symbol across all files",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()
		oldName := args[0]
		newName := args[1]
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		refs, err := index.FindReferences(ctx, db, oldName)
		if err != nil {
			return err
		}

		if len(refs) == 0 {
			output.Print(output.RenameResult{OldName: oldName, NewName: newName, DryRun: dryRun})
			return nil
		}

		grouped := make(map[string][]index.SymbolInfo)
		for _, r := range refs {
			grouped[r.File] = append(grouped[r.File], r)
		}

		var filesChanged []string
		for file := range grouped {
			filesChanged = append(filesChanged, output.Rel(file))
		}

		if dryRun {
			var preview []output.RenameOccurrence
			for _, r := range refs {
				src, err := os.ReadFile(r.File)
				if err != nil {
					continue
				}
				lines := strings.SplitAfter(string(src), "\n")
				lineIdx := int(r.StartLine) - 1
				lineText := ""
				if lineIdx >= 0 && lineIdx < len(lines) {
					lineText = strings.TrimRight(lines[lineIdx], "\n")
				}
				preview = append(preview, output.RenameOccurrence{
					File: output.Rel(r.File),
					Line: int(r.StartLine),
					Text: lineText,
				})
			}
			output.Print(output.RenameResult{
				OldName:      oldName,
				NewName:      newName,
				FilesChanged: filesChanged,
				Occurrences:  len(refs),
				DryRun:       true,
				Preview:      preview,
			})
			return nil
		}

		tx := edit.NewTransaction()
		for file, fileRefs := range grouped {
			hash, _ := edit.FileHash(file)
			for i, r := range fileRefs {
				h := ""
				if i == 0 {
					h = hash
				}
				tx.Add(file, r.StartByte, r.EndByte, newName, h)
			}
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("rename failed: %w", err)
		}

		hashes := make(map[string]string)
		for file := range grouped {
			_ = index.IndexFile(ctx, db, file)
			rel := output.Rel(file)
			if h, err := edit.FileHash(file); err == nil {
				hashes[rel] = h
			}
		}

		output.Print(output.RenameResult{
			OldName:      oldName,
			NewName:      newName,
			FilesChanged: filesChanged,
			Occurrences:  len(refs),
			Hashes:       hashes,
		})
		return nil
	},
}

// --- insert-after ---

var insertAfterCmd = &cobra.Command{
	Use:   "insert-after [file] <symbol>",
	Short: "Insert code after a symbol (reads from stdin)",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()
		sym, err := resolveSymbol(ctx, db, args)
		if err != nil {
			return err
		}

		content, err := readStdin()
		if err != nil {
			return fmt.Errorf("reading content from stdin: %w", err)
		}

		insertion := "\n\n" + content
		err = edit.InsertAfterSpan(sym.File, sym.EndByte, insertion)
		if err != nil {
			output.Print(output.EditResult{OK: false, File: output.Rel(sym.File), Message: err.Error()})
			return nil
		}

		_ = index.IndexFile(ctx, db, sym.File)
		hash, _ := edit.FileHash(sym.File)
		output.Print(output.EditResult{OK: true, File: output.Rel(sym.File), Message: fmt.Sprintf("inserted after %s", sym.Name), Hash: hash})
		return nil
	},
}

// --- append-file ---

var appendFileCmd = &cobra.Command{
	Use:   "append-file <file>",
	Short: "Append code to end of a file (reads from stdin)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := index.NormalizeRoot(getRoot(cmd))
		if err != nil {
			return err
		}
		file, err := index.ResolvePath(root, args[0])
		if err != nil {
			return err
		}

		content, err := readStdin()
		if err != nil {
			return fmt.Errorf("reading content from stdin: %w", err)
		}

		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}

		sep := "\n"
		if len(data) > 0 && data[len(data)-1] == '\n' {
			sep = ""
		}

		newData := append(data, []byte(sep+content+"\n")...)
		info, err := os.Stat(file)
		if err != nil {
			return err
		}
		if err := os.WriteFile(file, newData, info.Mode()); err != nil {
			return err
		}

		db, dbErr := openAndEnsureIndex(cmd)
		if dbErr == nil {
			ctx := context.Background()
			_ = index.IndexFile(ctx, db, file)
			db.Close()
		}

		hash, _ := edit.FileHash(file)
		output.Print(output.EditResult{OK: true, File: output.Rel(file), Message: fmt.Sprintf("appended %d bytes", len(content)), Hash: hash})
		return nil
	},
}

// --- smart-edit ---

var smartEditCmd = &cobra.Command{
	Use:   "smart-edit [file] <symbol>",
	Short: "Read, diff-preview, and replace a symbol in one step (reads replacement from stdin)",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()
		sym, err := resolveSymbol(ctx, db, args)
		if err != nil {
			return err
		}

		replacement, err := readStdin()
		if err != nil {
			return fmt.Errorf("reading replacement from stdin: %w", err)
		}

		src, err := os.ReadFile(sym.File)
		if err != nil {
			return err
		}
		oldBody := string(src[sym.StartByte:sym.EndByte])

		diff, err := edit.DiffPreview(sym.File, sym.StartByte, sym.EndByte, replacement)
		if err != nil {
			return err
		}

		hash, _ := edit.FileHash(sym.File)
		err = edit.ReplaceSpan(sym.File, sym.StartByte, sym.EndByte, replacement, hash)
		if err != nil {
			return fmt.Errorf("edit failed: %w", err)
		}

		_ = index.IndexFile(ctx, db, sym.File)

		newHash, _ := edit.FileHash(sym.File)
		output.Print(map[string]any{
			"ok":       true,
			"file":     output.Rel(sym.File),
			"symbol":   sym.Name,
			"diff":     diff,
			"hash":     newHash,
			"old_size": len(oldBody) / 4,
			"new_size": len(replacement) / 4,
		})
		return nil
	},
}

// --- find-files ---

var findFilesCmd = &cobra.Command{
	Use:   "find-files <pattern>",
	Short: "Find files by glob pattern (supports **)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := index.NormalizeRoot(getRoot(cmd))
		if err != nil {
			return err
		}
		output.SetRoot(root)
		dir, _ := cmd.Flags().GetString("dir")
		budget, _ := cmd.Flags().GetInt("budget")

		results, err := search.FindFiles(context.Background(), root, args[0], dir, budget)
		if err != nil {
			return err
		}
		output.Print(results)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(findFilesCmd)
	findFilesCmd.Flags().String("dir", "", "scope search to directory")
	findFilesCmd.Flags().Int("budget", 0, "limit results by total file size in tokens")
}

func init() {
	rootCmd.AddCommand(renameSymbolCmd)
	renameSymbolCmd.Flags().Bool("dry-run", false, "preview what would change without applying")
	rootCmd.AddCommand(insertAfterCmd)
	rootCmd.AddCommand(appendFileCmd)
	rootCmd.AddCommand(smartEditCmd)
	rootCmd.AddCommand(batchReadCmd)
	batchReadCmd.Flags().Int("budget", 0, "token budget (0 = unlimited)")
	batchReadCmd.Flags().Bool("symbols", false, "include symbol lists")
}

// --- batch-read ---

var batchReadCmd = &cobra.Command{
	Use:   "batch-read <file-or-file:symbol> ...",
	Short: "Read multiple files/symbols in one call",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		budget, _ := cmd.Flags().GetInt("budget")
		showSymbols, _ := cmd.Flags().GetBool("symbols")
		flags := map[string]any{"budget": budget, "symbols": showSymbols}

		result, err := dispatch.Dispatch(context.Background(), db, "batch-read", args, flags)
		if err != nil {
			return err
		}
		output.Print(result)
		return nil
	},
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
