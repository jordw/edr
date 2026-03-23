package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/setup"
	"github.com/spf13/cobra"
)

var setupCmd = &cobra.Command{
	Use:   "setup [path]",
	Short: "Index repo and install global agent instructions",
	Long: `Index the repository and install edr instructions globally.

Writes a versioned instruction block to agent config files:
  ~/.claude/CLAUDE.md    (Claude Code)
  ~/.codex/AGENTS.md     (Codex)
  ~/.cursor/rules/edr.mdc (Cursor)

The block is wrapped in sentinel comments and can be surgically updated on future runs.

Flags:
  --force      Replace existing instructions with the latest version
  --status     Show what's installed without modifying anything
  --uninstall  Remove edr instructions from all global configs
  --generic    Print instructions to stdout (for other agents)`,
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
	setupCmd.Flags().Bool("status", false, "Show installation status without modifying anything")
	setupCmd.Flags().Bool("uninstall", false, "Remove edr instructions from all global configs")
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
	jsonOut, _ := cmd.Flags().GetBool("json")
	generic, _ := cmd.Flags().GetBool("generic")
	statusOnly, _ := cmd.Flags().GetBool("status")
	uninstall, _ := cmd.Flags().GetBool("uninstall")

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

	// --status: show what's installed and exit.
	if statusOnly {
		status := setup.GlobalStatus(BuildHash)
		result.Global = status
		if !jsonOut {
			// Plain-mode transport: JSON header on stdout, body with details.
			fmt.Printf("{\"current_hash\":%q,\"targets\":%d}\n", BuildHash, len(status))
			for _, s := range status {
				state := "not installed"
				if s.AlreadyCurrent {
					state = fmt.Sprintf("current (hash:%s)", s.InstalledHash)
				} else if s.Outdated {
					state = fmt.Sprintf("outdated (installed:%s current:%s)", s.InstalledHash, BuildHash)
				}
				fmt.Printf("  %s: %s\n", s.Path, state)
			}
			return nil
		}
		return printSetupOutput(result, jsonOut)
	}

	// --uninstall: remove edr instructions and exit.
	if uninstall {
		results, _ := setup.UninstallAllGlobal()
		result.Global = results
		if !jsonOut {
			for _, r := range results {
				if r.Error != "" {
					fmt.Fprintf(os.Stderr, "  %s: error: %s\n", r.Target, r.Error)
				} else if r.Removed {
					fmt.Fprintf(os.Stderr, "  removed from %s\n", r.Path)
				} else {
					fmt.Fprintf(os.Stderr, "  %s: not installed\n", r.Path)
				}
			}
		}
		return printSetupOutput(result, jsonOut)
	}

	// Resolve target path.
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

	// Validate target path exists.
	info, err := os.Stat(root)
	if err != nil {
		result.Error = fmt.Sprintf("target path does not exist: %s", root)
		return printSetupOutput(result, jsonOut)
	}
	if !info.IsDir() {
		result.Error = fmt.Sprintf("target path is not a directory: %s", root)
		return printSetupOutput(result, jsonOut)
	}

	force, _ := cmd.Flags().GetBool("force")
	globalExplicit, _ := cmd.Flags().GetBool("global")
	noGlobal, _ := cmd.Flags().GetBool("no-global")

	// Step 1: Create .edr/ and ensure it's in .gitignore.
	os.MkdirAll(filepath.Join(root, ".edr"), 0700)
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
	if !shouldInstall {
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

		if jsonOut {
			// JSON mode: report status without prompting or installing.
			result.Global = status
			return printSetupOutput(result, jsonOut)
		}

		// Prompt user.
		shouldInstall = promptGlobalInstall(status)
	}

	if shouldInstall {
		// User consented (or --force/--global) — always force to update outdated blocks.
		results, _ := setup.InjectAllGlobal(BuildHash, true)
		result.Global = results
		_ = setup.WriteSentinel(BuildHash)

		if !jsonOut {
			for _, r := range results {
				if r.Error != "" {
					fmt.Fprintf(os.Stderr, "  %s: error: %s\n", r.Target, r.Error)
				} else if r.AlreadyCurrent {
					fmt.Fprintf(os.Stderr, "  %s up-to-date (hash:%s)\n", r.Path, BuildHash)
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
		fmt.Fprintf(os.Stderr, "edr: %s\n", r.Error)
		return silentError{code: 1}
	}
	return nil
}
