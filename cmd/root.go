package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/session"
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
	Args:          cobra.ArbitraryArgs,
	RunE:          rootRunE,
}

func rootRunE(cmd *cobra.Command, args []string) error {
	// If called with a JSON arg or stdin has data, route to handleDo.
	var raw json.RawMessage
	if len(args) > 0 && strings.HasPrefix(strings.TrimSpace(args[0]), "{") {
		raw = json.RawMessage(args[0])
	} else if len(args) == 0 {
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("reading stdin: %w", err)
			}
			if len(data) > 0 && strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
				raw = json.RawMessage(data)
			}
		}
	}

	if raw == nil {
		return cmd.Help()
	}

	db, err := openAndEnsureIndex(cmd)
	if err != nil {
		return err
	}
	defer db.Close()

	sess := session.New()
	ctx := context.Background()
	text, err := handleDo(ctx, db, sess, raw)
	if err != nil {
		return err
	}

	var out any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		fmt.Println(text)
	} else {
		output.Print(out)
	}
	return nil
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

	needsIndex := files == 0
	if !needsIndex {
		if stale, _ := index.HasStaleFiles(ctx, db); stale {
			needsIndex = true
		}
	}

	if needsIndex {
		if !quiet {
			if files == 0 {
				fmt.Fprintf(os.Stderr, "edr: no index found, indexing repository...\n")
			} else {
				fmt.Fprintf(os.Stderr, "edr: index stale, re-indexing...\n")
			}
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
