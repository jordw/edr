package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/setup"
	"github.com/spf13/cobra"
)

// Version and BuildHash are set at build time via ldflags:
//
//	go build -ldflags "-X github.com/jordw/edr/cmd.Version=... -X github.com/jordw/edr/cmd.BuildHash=..."
//
// For dev builds, BuildHash falls back to the current git HEAD.
var (
	Version   = "dev"
	BuildHash = ""
)

func init() {
	if BuildHash == "" {
		BuildHash = gitHead()
	}
}

// gitHead returns the short git commit hash, or "unknown" if git is unavailable.
func gitHead() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

var rootCmd = &cobra.Command{
	Use:           "edr",
	Short:         "The editor for agents",
	Long:          "Context-preserving code navigation and editing tool optimized for LLM agents.",
	Version:       Version,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		setup.AutoUpdate(BuildHash)
	},
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
		if ie, ok := err.(*IndexError); ok {
			output.ErrorEnvelope(cmdName, ie.Code, ie.Message)
		} else {
			msg := err.Error()
			if strings.Contains(msg, "no such file or directory") {
				output.ErrorEnvelope(cmdName, "file_not_found", msg)
			} else {
				output.ErrorEnvelope(cmdName, "command_error", msg)
			}
		}
		os.Exit(1)
	}
}

// knownCommands is the set of valid edr subcommand names.
var knownCommands = map[string]bool{
	"read": true, "write": true, "edit": true, "map": true,
	"search": true, "refs": true, "rename": true, "verify": true,
	"reindex": true, "setup": true, "batch": true,
}

// detectCommandName extracts the subcommand name from os.Args.
// Only returns recognized command names — never file paths or other arguments.
// Skips global flags and their values using the same logic as findBatchFlag.
func detectCommandName() string {
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "--") {
			if strings.Contains(a, "=") {
				continue // --flag=value
			}
			if a == "--verbose" {
				continue // boolean, no value
			}
			i++ // skip value arg
			continue
		}
		if strings.HasPrefix(a, "-") && !IsBatchFlag(a) {
			continue // short flag we don't recognize
		}
		if knownCommands[a] {
			return a
		}
		break // first non-flag arg is not a known command
	}
	return "batch" // implicit batch mode if no subcommand found
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

// openDBStrict opens the DB and returns an error if the index does not exist.
// Read-only commands (read, search, map, refs) use this — they never auto-index.
func openDBStrict(cmd *cobra.Command) (*index.DB, error) {
	return openDBStrictRoot(getRoot(cmd))
}

// IndexError is a structured error for missing or empty index.
// It carries an error code ("no_index" or "empty_index") so that
// callers can produce structured JSON output with the correct code.
type IndexError struct {
	Code    string // "no_index" or "empty_index"
	Message string
}

func (e *IndexError) Error() string { return e.Message }

// openDBStrictRoot opens the DB, validates the index exists, and returns an error if not.
func openDBStrictRoot(root string) (*index.DB, error) {
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
		// Auto-index on first use instead of failing.
		indexed := false
		if lockErr := db.WithWriteLock(func() error {
			// Recheck after acquiring lock — another process may have indexed.
			currentFiles, _, _ := db.Stats(ctx)
			if currentFiles > 0 {
				return nil
			}
			fmt.Fprintf(os.Stderr, "edr: no index found, indexing repository...\n")
			_, _, e := index.IndexRepo(ctx, db)
			indexed = e == nil
			return e
		}); lockErr != nil {
			db.Close()
			return nil, lockErr
		}
		if indexed {
			totalFiles, totalSyms, _ := db.Stats(ctx)
			fmt.Fprintf(os.Stderr, "edr: index ready (%d files, %d symbols)\n", totalFiles, totalSyms)
		}
	}
	output.SetRoot(db.Root())
	return db, nil
}

// openDBAndIndex opens the DB and bootstraps the index if needed.
// Only used by reindex, setup, and batch (which may contain edits that need verification).
func openDBAndIndex(root string, quiet bool) (*index.DB, error) {
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
	if root == "." || root == "" {
		wd, err := os.Getwd()
		if err == nil {
			root = discoverRoot(wd)
		}
	}
	return root
}

// discoverRoot walks up from dir looking for .edr/ or .git/ to find the repo root.
// Falls back to dir itself if no marker is found.
func discoverRoot(dir string) string {
	cur := dir
	for {
		if _, err := os.Stat(filepath.Join(cur, ".edr")); err == nil {
			return cur
		}
		if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break // reached filesystem root
		}
		cur = parent
	}
	return dir // fallback to original dir
}
