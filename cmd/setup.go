package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/jordw/edr/internal/setup"
	"github.com/spf13/cobra"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Index repo and configure agent instructions",
	Long: `Index the repository and inject edr instructions into your agent's config file.

Auto-detects the target if CLAUDE.md, .cursorrules, or AGENTS.md exists.
Use --claude, --cursor, --codex, or --generic to specify explicitly.`,
	RunE: runSetup,
}

func init() {
	setupCmd.Flags().Bool("claude", false, "Configure CLAUDE.md")
	setupCmd.Flags().Bool("cursor", false, "Configure .cursorrules")
	setupCmd.Flags().Bool("codex", false, "Configure AGENTS.md")
	setupCmd.Flags().Bool("generic", false, "Print instructions to stdout")
	setupCmd.Flags().Bool("skip-index", false, "Skip indexing (only inject instructions)")
}

type setupResult struct {
	Indexed      bool   `json:"indexed,omitempty"`
	Target       string `json:"target"`
	File         string `json:"file,omitempty"`
	Gitignore    bool   `json:"gitignore,omitempty"`
	Instructions string `json:"instructions,omitempty"` // only for --generic
	Error        string `json:"error,omitempty"`
}

func runSetup(cmd *cobra.Command, args []string) error {
	root := getRoot(cmd)

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
		// Auto-detect.
		target = setup.DetectTarget(root)
		if target == "" {
			// Default to claude if nothing detected.
			target = setup.TargetClaude
		}
	}

	result := setupResult{Target: string(target)}

	// Step 1: Index (unless --skip-index).
	skipIndex, _ := cmd.Flags().GetBool("skip-index")
	if !skipIndex {
		db, err := openDBWithRoot(root, false)
		if err != nil {
			result.Error = fmt.Sprintf("index failed: %v", err)
			return printSetupResult(result)
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

	// Step 3: Inject instructions.
	if target == setup.TargetGeneric {
		text, err := setup.Instructions(target)
		if err != nil {
			result.Error = err.Error()
			return printSetupResult(result)
		}
		result.Instructions = text
		return printSetupResult(result)
	}

	path, err := setup.InjectInstructions(root, target)
	if err != nil {
		result.Error = err.Error()
		result.File = path
		return printSetupResult(result)
	}
	result.File = path
	return printSetupResult(result)
}

func printSetupResult(r setupResult) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
