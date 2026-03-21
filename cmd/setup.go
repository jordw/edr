package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/setup"
	"github.com/spf13/cobra"
)

var setupCmd = &cobra.Command{
	Use:   "setup [path]",
	Short: "Index repo and install global agent instructions",
	Long: `Index the repository and install edr instructions globally.

Writes a versioned instruction block to ~/.claude/CLAUDE.md and ~/.codex/AGENTS.md
so all agent sessions use edr for file operations.

The block is wrapped in sentinel comments and can be surgically updated on future runs.
Use --force to replace existing instructions with the latest version.
Use --generic to print instructions to stdout (for other agents).`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSetup,
}

func init() {
	setupCmd.Flags().Bool("global", false, "Install global instructions without prompting")
	setupCmd.Flags().Bool("no-global", false, "Skip global instruction prompt")
	setupCmd.Flags().Bool("generic", false, "Print instructions to stdout")
	setupCmd.Flags().Bool("force", false, "Replace existing edr instructions with latest version")
	setupCmd.Flags().Bool("skip-index", false, "Skip indexing (only install instructions)")
	setupCmd.Flags().Bool("json", false, "Output JSON instead of human-readable text")
}

type setupResult struct {
	Indexed      bool                 `json:"indexed,omitempty"`
	Gitignore    bool                 `json:"gitignore,omitempty"`
	Global       []setup.InjectResult `json:"global,omitempty"`
	Instructions string               `json:"instructions,omitempty"` // only for --generic
	CurrentHash  string               `json:"current_hash,omitempty"`
	Error        string               `json:"error,omitempty"`
}

func runSetup(cmd *cobra.Command, args []string) error {
	root := getRoot(cmd)
	if len(args) > 0 && args[0] != "" {
		root = args[0]
	}
	if root == "" || root == "." {
		wd, err := os.Getwd()
		if err == nil {
			root = wd
		}
	}

	force, _ := cmd.Flags().GetBool("force")
	jsonOut, _ := cmd.Flags().GetBool("json")
	generic, _ := cmd.Flags().GetBool("generic")
	globalExplicit, _ := cmd.Flags().GetBool("global")
	noGlobal, _ := cmd.Flags().GetBool("no-global")

	result := setupResult{CurrentHash: BuildHash}

	// --generic: just print instructions to stdout and exit.
	if generic {
		text, err := setup.Instructions(setup.TargetGeneric)
		if err != nil {
			result.Error = err.Error()
			return printSetupOutput(result, jsonOut)
		}
		result.Instructions = text
		return printSetupOutput(result, jsonOut)
	}

	// Step 1: Index (unless --skip-index).
	skipIndex, _ := cmd.Flags().GetBool("skip-index")
	if !skipIndex {
		db, err := openDBAndIndex(root, jsonOut)
		if err != nil {
			result.Error = fmt.Sprintf("index failed: %v", err)
			return printSetupOutput(result, jsonOut)
		}
		db.Close()
		result.Indexed = true
	}

	// Step 2: Ensure .edr/ in .gitignore.
	if err := setup.EnsureGitignore(root); err != nil {
		fmt.Fprintf(os.Stderr, "warning: gitignore: %v\n", err)
	} else {
		result.Gitignore = true
	}

	// Step 3: Global instructions.
	if noGlobal {
		return printSetupOutput(result, jsonOut)
	}

	shouldInstall := globalExplicit || force
	if !shouldInstall && !jsonOut {
		// Check current status to decide whether to prompt.
		status := setup.GlobalStatus(BuildHash)
		allCurrent := true
		for _, s := range status {
			if !s.AlreadyCurrent {
				allCurrent = false
				break
			}
		}
		if allCurrent {
			// Already up to date — just report and skip.
			result.Global = status
			return printSetupOutput(result, jsonOut)
		}

		// Prompt user.
		shouldInstall = promptGlobalInstall(status)
	}

	if shouldInstall {
		results, _ := setup.InjectAllGlobal(BuildHash, force)
		result.Global = results
		// Record opt-in so future edr runs auto-update.
		_ = setup.WriteSentinel(BuildHash)

		if !jsonOut {
			for _, r := range results {
				if r.Error != "" {
					fmt.Fprintf(os.Stderr, "  %s: error: %s\n", r.Target, r.Error)
				} else if r.AlreadyCurrent {
					fmt.Fprintf(os.Stderr, "  %s up-to-date (hash:%s)\n", r.Path, BuildHash)
				} else if r.Outdated {
					fmt.Fprintf(os.Stderr, "  %s outdated (installed:%s current:%s)\n", r.Path, r.InstalledHash, BuildHash)
					fmt.Fprintf(os.Stderr, "  run: edr setup --force\n")
				} else if r.Updated {
					fmt.Fprintf(os.Stderr, "  updated %s (hash:%s)\n", r.Path, BuildHash)
				} else if r.Created {
					fmt.Fprintf(os.Stderr, "  wrote %s (hash:%s)\n", r.Path, BuildHash)
				}
			}
		}
	}

	return printSetupOutput(result, jsonOut)
}

// promptGlobalInstall asks the user whether to install global instructions.
func promptGlobalInstall(status []setup.InjectResult) bool {
	fmt.Fprintf(os.Stderr, "\n  Install edr instructions globally? This writes a short block to:\n")
	for _, s := range status {
		state := "new"
		if s.Outdated {
			state = fmt.Sprintf("outdated, installed:%s", s.InstalledHash)
		} else if s.AlreadyCurrent {
			state = "up-to-date"
		}
		fmt.Fprintf(os.Stderr, "    %s (%s)\n", s.Path, state)
	}
	fmt.Fprintf(os.Stderr, "  [y/N]: ")

	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
		return answer == "y" || answer == "yes"
	}
	return false
}

func printSetupOutput(r setupResult, jsonOut bool) error {
	if jsonOut {
		env := output.NewEnvelope("setup")
		env.AddOp("s0", "setup", r)
		if r.Error != "" {
			env.OK = false
			env.AddError("setup_error", r.Error)
		} else {
			env.OK = true
		}
		output.PrintEnvelope(env)
		if !env.OK {
			return silentError{code: 1}
		}
		return nil
	}
	// For generic target, print instructions to stdout even in human mode.
	if r.Instructions != "" {
		fmt.Print(r.Instructions)
	}
	if r.Error != "" {
		return fmt.Errorf("%s", r.Error)
	}
	return nil
}
