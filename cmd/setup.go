package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jordw/edr/internal/setup"
	"github.com/spf13/cobra"
)

var setupCmd = &cobra.Command{
	Use:   "setup [path]",
	Short: "Index repo and configure agent instructions",
	Long: `Index the repository and inject edr instructions into your agent's config file.

Auto-detects the target if CLAUDE.md, .cursorrules, or AGENTS.md exists.
Use --claude, --cursor, --codex, or --generic to specify explicitly.
Use --force to update previously injected instructions to the latest version.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSetup,
}

func init() {
	setupCmd.Flags().Bool("claude", false, "Configure CLAUDE.md")
	setupCmd.Flags().Bool("cursor", false, "Configure .cursorrules")
	setupCmd.Flags().Bool("codex", false, "Configure AGENTS.md")
	setupCmd.Flags().Bool("generic", false, "Print instructions to stdout")
	setupCmd.Flags().Bool("force", false, "Replace existing edr instructions with latest version")
	setupCmd.Flags().Bool("skip-index", false, "Skip indexing (only inject instructions)")
	setupCmd.Flags().Bool("json", false, "Output JSON instead of human-readable text")
}

type setupResult struct {
	Indexed        bool   `json:"indexed,omitempty"`
	Target         string `json:"target"`
	File           string `json:"file,omitempty"`
	AlreadyCurrent bool   `json:"already_current,omitempty"`
	Outdated       bool   `json:"outdated,omitempty"`
	InstalledHash  string `json:"installed_hash,omitempty"`
	CurrentHash    string `json:"current_hash,omitempty"`
	Updated        bool   `json:"updated,omitempty"`
	Gitignore      bool   `json:"gitignore,omitempty"`
	Instructions   string `json:"instructions,omitempty"` // only for --generic
	Error          string `json:"error,omitempty"`
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

	// Determine target.
	var target setup.Target
	if b, _ := cmd.Flags().GetBool("claude"); b {
		target = setup.TargetClaude
	} else if b, _ := cmd.Flags().GetBool("cursor"); b {
		target = setup.TargetCursor
	} else if b, _ := cmd.Flags().GetBool("codex"); b {
		target = setup.TargetCodex
	} else if b, _ := cmd.Flags().GetBool("generic"); b {
		target = setup.TargetGeneric
	} else {
		target = setup.DetectTarget(root)
		if target == "" {
			target = setup.TargetGeneric
			if !jsonOut {
				fmt.Fprintf(os.Stderr, "edr: no agent config detected, printing instructions to stdout\n")
				fmt.Fprintf(os.Stderr, "edr: use --claude, --cursor, or --codex to write to a specific file\n")
			}
		}
	}

	result := setupResult{Target: string(target), CurrentHash: BuildHash}

	// Step 1: Index (unless --skip-index).
	skipIndex, _ := cmd.Flags().GetBool("skip-index")
	if !skipIndex {
		db, err := openDBWithRoot(root, false)
		if err != nil {
			result.Error = fmt.Sprintf("index failed: %v", err)
			return printSetupOutput(result, jsonOut)
		}
		db.Close()
		result.Indexed = true
		if !jsonOut {
			fmt.Fprintf(os.Stderr, "  indexed %s\n", filepath.Base(root))
		}
	}

	// Step 2: Ensure .edr/ in .gitignore.
	if err := setup.EnsureGitignore(root); err != nil {
		fmt.Fprintf(os.Stderr, "warning: gitignore: %v\n", err)
	} else {
		result.Gitignore = true
		if !jsonOut {
			fmt.Fprintf(os.Stderr, "  added .edr/ to .gitignore\n")
		}
	}

	// Step 3: Inject instructions.
	if target == setup.TargetGeneric {
		text, err := setup.Instructions(target)
		if err != nil {
			result.Error = err.Error()
			return printSetupOutput(result, jsonOut)
		}
		result.Instructions = text
		return printSetupOutput(result, jsonOut)
	}

	ir, err := setup.InjectInstructions(root, target, BuildHash, force)
	if err != nil {
		result.Error = err.Error()
		return printSetupOutput(result, jsonOut)
	}
	result.File = ir.Path
	result.AlreadyCurrent = ir.AlreadyCurrent
	result.Outdated = ir.Outdated
	result.InstalledHash = ir.InstalledHash
	result.Updated = ir.Updated

	if !jsonOut {
		file := setup.ConfigFile(target)
		switch {
		case ir.AlreadyCurrent:
			fmt.Fprintf(os.Stderr, "  %s up-to-date (hash:%s)\n", file, BuildHash)
		case ir.Outdated:
			installed := ir.InstalledHash
			if installed == "" {
				installed = "none"
			}
			fmt.Fprintf(os.Stderr, "  %s outdated (installed:%s current:%s)\n", file, installed, BuildHash)
			fmt.Fprintf(os.Stderr, "  run: edr setup --force\n")
		case ir.Updated:
			fmt.Fprintf(os.Stderr, "  updated %s (hash:%s)\n", file, BuildHash)
		default:
			fmt.Fprintf(os.Stderr, "  wrote %s (hash:%s)\n", file, BuildHash)
		}
	}

	return printSetupOutput(result, jsonOut)
}

func printSetupOutput(r setupResult, jsonOut bool) error {
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
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
