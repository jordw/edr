package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/spf13/cobra"
)

// Version and BuildHash are set at build time via ldflags:
//
//	go build -ldflags "-X github.com/jordw/edr/cmd.Version=... -X github.com/jordw/edr/cmd.BuildHash=..."
var (
	Version   = "dev"
	BuildHash = "unknown"
)

var rootCmd = &cobra.Command{
	Use:           "edr",
	Short:         "The editor for agents",
	Long:          "Context-preserving code navigation and editing tool optimized for LLM agents.",
	Version:       Version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		// Emit structured JSON error to stdout for machine-friendly parsing,
		// matching the MCP error shape. Keep non-zero exit code for shell chaining.
		errJSON, _ := json.Marshal(err.Error())
		fmt.Fprintf(os.Stdout, "{\"ok\":false,\"error\":%s}\n", errJSON)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringP("root", "r", ".", "repository root directory")
}

// openAndEnsureIndex opens the DB and bootstraps the index if it does not exist yet.
// It silently indexes without stderr output in quiet mode (batch/MCP).
func openAndEnsureIndex(cmd *cobra.Command) (*index.DB, error) {
	return openDB(cmd, false)
}

// openAndEnsureIndexQuiet opens the DB and indexes silently (for batch/MCP).
func openAndEnsureIndexQuiet(cmd *cobra.Command) (*index.DB, error) {
	return openDB(cmd, true)
}

func openDB(cmd *cobra.Command, quiet bool) (*index.DB, error) {
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
		if !quiet {
			fmt.Fprintf(os.Stderr, "edr: no index found, indexing repository...\n")
		}
		var filesIndexed, symbolsFound int
		err = db.WithWriteLock(func() error {
			var e error
			filesIndexed, symbolsFound, e = index.IndexRepo(ctx, db)
			return e
		})
		if err != nil {
			db.Close()
			return nil, err
		}
		if !quiet {
			fmt.Fprintf(os.Stderr, "edr: indexed %d files, %d symbols\n", filesIndexed, symbolsFound)
		}
	}

	output.SetRoot(db.Root())
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
