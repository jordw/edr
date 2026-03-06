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
