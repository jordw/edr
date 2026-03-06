package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/jordw/edr/internal/index"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "edr",
	Short: "Edit, Discover, Refactor — a CLI for coding agents",
	Long:  "Context-preserving code navigation and editing tool optimized for LLM agents.",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringP("root", "r", ".", "repository root directory")
}

// openAndEnsureIndex opens the DB and automatically indexes if the index is empty or stale.
func openAndEnsureIndex(cmd *cobra.Command) (*index.DB, error) {
	root := getRoot(cmd)
	db, err := index.OpenDB(root)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	files, _, err := db.Stats(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}

	if files == 0 {
		fmt.Fprintf(os.Stderr, "edr: no index found, indexing repository...\n")
		filesIndexed, symbolsFound, err := index.IndexRepo(ctx, db)
		if err != nil {
			db.Close()
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "edr: indexed %d files, %d symbols\n", filesIndexed, symbolsFound)
	}

	return db, nil
}

// resolveSymbol accepts 1 or 2 args: either "<symbol>" or "<file> <symbol>".
// With 1 arg, it resolves globally (errors if ambiguous).
// With 2 args, it looks up in the specific file.
func resolveSymbol(ctx context.Context, db *index.DB, args []string) (*index.SymbolInfo, error) {
	root := db.Root()
	switch len(args) {
	case 1:
		return db.ResolveSymbol(ctx, args[0])
	case 2:
		file := args[0]
		if len(file) > 0 && file[0] != '/' {
			file = root + "/" + file
		}
		return db.GetSymbol(ctx, file, args[1])
	default:
		return nil, fmt.Errorf("expected 1 or 2 arguments: [file] <symbol>")
	}
}

func getRoot(cmd *cobra.Command) string {
	root, _ := cmd.Flags().GetString("root")
	if root == "." {
		wd, err := os.Getwd()
		if err == nil {
			root = wd
		}
	}
	return root
}
