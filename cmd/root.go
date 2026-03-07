package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
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

	// Serialize DB open + migration + initial indexing across parallel CLI
	// processes using a file lock. This prevents SQLITE_BUSY errors during
	// first-use bootstrap when agents fire multiple writes in parallel.
	edrDir := filepath.Join(index.NormalizeRootOrDefault(root), ".edr")
	os.MkdirAll(edrDir, 0755)
	lockPath := filepath.Join(edrDir, "index.lock")
	lockFile, lockErr := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if lockErr == nil {
		if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
			// Flock not supported (e.g., network FS) — fall through without lock
			lockFile.Close()
			lockFile = nil
		} else {
			defer func() {
				syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
				lockFile.Close()
			}()
		}
	}

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
		filesIndexed, symbolsFound, err := index.IndexRepo(ctx, db)
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
