package cmd

import (
	"context"
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
		// Detect command name from os.Args for structured error output.
		cmdName := detectCommandName()
		output.ErrorEnvelope(cmdName, "command_error", err.Error())
		os.Exit(1)
	}
}

// detectCommandName extracts the subcommand name from os.Args.
func detectCommandName() string {
	for _, arg := range os.Args[1:] {
		if len(arg) > 0 && arg[0] != '-' {
			return arg
		}
	}
	return ""
}

var verbose bool

func init() {
	rootCmd.PersistentFlags().String("root", ".", "repository root directory")
	rootCmd.PersistentFlags().BoolVar(&verbose, "verbose", false, "emit diagnostics to stderr")

	// Hide auto-generated commands from help output
	rootCmd.CompletionOptions.HiddenDefaultCmd = true
	rootCmd.SetHelpCommand(&cobra.Command{Hidden: true})
}

// Verbose returns true if --verbose was set.
func Verbose() bool { return verbose }

// openAndEnsureIndex opens the DB and bootstraps the index if it does not exist yet.
func openAndEnsureIndex(cmd *cobra.Command) (*index.DB, error) {
	return openDB(cmd, !verbose)
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
		if indexed {
			totalFiles, totalSyms, _ := db.Stats(ctx)
			if !quiet {
				fmt.Fprintf(os.Stderr, "\redr: index ready (%d files, %d symbols)\n", totalFiles, totalSyms)
			}
			if totalFiles == 0 && !quiet {
				fmt.Fprintf(os.Stderr, "edr: warning: 0 files indexed in %s\n", root)
			}
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
