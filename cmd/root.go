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
		// Silent errors (e.g. batch with failed operations) already printed
		// their structured output — just exit non-zero.
		if se, ok := err.(silentError); ok {
			os.Exit(se.ExitCode())
		}
		// Emit structured JSON error to stdout for machine-friendly parsing.
		// Keep non-zero exit code for shell chaining.
		errJSON, _ := json.Marshal(err.Error())
		fmt.Fprintf(os.Stdout, "{\"ok\":false,\"error\":%s}\n", errJSON)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().String("root", ".", "repository root directory")
}

// openAndEnsureIndex opens the DB and bootstraps the index if it does not exist yet.
func openAndEnsureIndex(cmd *cobra.Command) (*index.DB, error) {
	return openDB(cmd, false)
}

func openDB(cmd *cobra.Command, quiet bool) (*index.DB, error) {
	return openDBWithRoot(getRoot(cmd), quiet)
}

// openDBWithRoot is the core DB opener, usable without a cobra command.
func openDBWithRoot(root string, quiet bool) (*index.DB, error) {
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
		indexed := false
		err = db.WithWriteLock(func() error {
			// Recheck after acquiring lock — another process may have indexed already
			currentFiles, _, _ := db.Stats(ctx)
			if firstIndex && currentFiles > 0 {
				return nil
			}
			if !firstIndex {
				if stale, _ := index.HasStaleFiles(ctx, db); !stale {
					return nil
				}
			}
			if !quiet {
				if firstIndex {
					fmt.Fprintf(os.Stderr, "edr: no index found, indexing repository...\n")
				} else {
					fmt.Fprintf(os.Stderr, "edr: index stale, re-indexing...\n")
				}
			}
			var progress index.ProgressFunc
			if !quiet {
				progress = func(files, symbols int) {
					fmt.Fprintf(os.Stderr, "\redr: indexed %d files (%d symbols)...", files, symbols)
				}
			}
			_, _, e := index.IndexRepo(ctx, db, progress)
			indexed = e == nil
			return e
		})
		if err != nil {
			db.Close()
			return nil, err
		}
		if !quiet && indexed {
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
