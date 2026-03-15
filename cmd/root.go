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
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		// Emit structured JSON error to stdout for machine-friendly parsing.
		// Keep non-zero exit code for shell chaining.
		errJSON, _ := json.Marshal(err.Error())
		fmt.Fprintf(os.Stdout, "{\"ok\":false,\"error\":%s}\n", errJSON)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringP("root", "r", ".", "repository root directory")
	rootCmd.PersistentFlags().Bool("no-proxy", false, "bypass running server, use ephemeral dispatch")
}

// openAndEnsureIndex opens the DB and bootstraps the index if it does not exist yet.
// It silently indexes without stderr output in quiet mode.
func openAndEnsureIndex(cmd *cobra.Command) (*index.DB, error) {
	return openDB(cmd, false)
}

// openAndEnsureIndexQuiet opens the DB and indexes silently (for serve mode).
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

	needsIndex := files == 0
	if !needsIndex {
		if stale, _ := index.HasStaleFiles(ctx, db); stale {
			needsIndex = true
		}
	}

	if needsIndex {
		firstIndex := files == 0
		if !quiet {
			if firstIndex {
				fmt.Fprintf(os.Stderr, "edr: no index found, indexing repository...\n")
			} else {
				fmt.Fprintf(os.Stderr, "edr: index stale, re-indexing...\n")
			}
		}
		err = db.WithWriteLock(func() error {
			// Re-check after acquiring the lock — another process may have
			// already completed the index/re-index while we waited.
			currentFiles, _, _ := db.Stats(ctx)
			if firstIndex && currentFiles > 0 {
				return nil // another process completed the first index
			}
			if !firstIndex {
				if stale, _ := index.HasStaleFiles(ctx, db); !stale {
					return nil // already up to date
				}
			}
			var progress index.ProgressFunc
			if !quiet {
				progress = func(files, symbols int) {
					fmt.Fprintf(os.Stderr, "\redr: indexed %d files (%d symbols)...", files, symbols)
				}
			}
			_, _, e := index.IndexRepo(ctx, db, progress)
			return e
		})
		if err != nil {
			db.Close()
			return nil, err
		}
		if !quiet {
			// Report total index size, not just changed files — avoids
			// confusing "indexed 0 files" when everything was already current.
			totalFiles, totalSyms, _ := db.Stats(ctx)
			fmt.Fprintf(os.Stderr, "\redr: index ready (%d files, %d symbols)\n", totalFiles, totalSyms)
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
