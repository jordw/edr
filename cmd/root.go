package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/idx"
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
	Long: `Code editing tools for agents.

Batch mode (primary interface):
  edr -f file[:Symbol]              Focus on file or symbol
  edr -o [--dir path]               Orient: structural overview
  edr -e file --old X --new Y       Edit file
  edr -f file:Sym -e --old X --new Y   Focus then edit (one call)

Standalone commands are listed below.`,
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
		// Silent errors from run (subprocess exit code) and setup —
		// these are the only commands that use non-zero exit codes.
		if se, ok := err.(silentError); ok {
			os.Exit(se.ExitCode())
		}
		// All other errors: emit structured JSON so the agent can parse it.
		// Exit 0 because the agent reads ok:false from the output;
		// non-zero exit codes just alarm the human watching the terminal.
		cmdName := detectCommandName()
		msg := err.Error()
		if strings.Contains(msg, "no such file or directory") {
			output.ErrorEnvelope(cmdName, "file_not_found", msg)
		} else {
			output.ErrorEnvelope(cmdName, "command_error", msg)
		}
	}
}

// knownCommands is the set of valid edr subcommand names.
var knownCommands = map[string]bool{
	"read": true, "write": true, "edit": true, "map": true,
	"search": true, "refs": true, "rename": true, "verify": true,
	"reset": true, "setup": true, "batch": true, "index": true, "files": true,
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

// openStore returns a SymbolStore for the given root.
// Also does a small amount of incremental trigram indexing per invocation.
func openStore(root string) (index.SymbolStore, error) {
	output.SetRoot(root)
	db := index.NewOnDemand(root)
	idx.IncrementalTick(root, db.EdrDir(), 50, index.WalkRepoFiles)
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
	// Normalize early so that @file resolution and DB open see the same root.
	if normalized, err := index.NormalizeRoot(root); err == nil {
		root = normalized
	}
	return root
}

// discoverRoot walks up from dir looking for .git/ to find the repo root.
// Falls back to dir itself if no marker is found.
func discoverRoot(dir string) string {
	cur := dir
	for {
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
