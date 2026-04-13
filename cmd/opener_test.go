package cmd

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestOpenerAlignment is a source-level test that verifies each command uses
// the correct DB opener. This prevents regressions when new commands are added.
//
// Categories:
//   - Strict (openDBStrict): orient, focus — via dispatchCmd
//   - Strict+stdin:          edit — via dispatchCmdWithStdin (also uses openDBStrict)
//   - Own opener:            setup — manages its own DB in setup.go
func TestOpenerAlignment(t *testing.T) {
	src, err := os.ReadFile("commands.go")
	if err != nil {
		t.Fatalf("reading commands.go: %v", err)
	}
	source := string(src)

	// Commands that must use dispatchCmd (calls openDBStrict internally)
	dispatchCmdCommands := []struct {
		varName string
		cmdName string
	}{
		{"orientCmd", "orient"},
		{"focusCmd", "focus"},
		{"renameCmd", "rename"},
		{"extractCmd", "extract"},
		{"indexCmd", "index"},
		{"filesCmd", "files"},
	}

	// Commands that must use dispatchCmdWithStdin (also calls openDBStrict internally)
	dispatchCmdWithStdinCommands := []struct {
		varName string
		cmdName string
	}{
		{"editCmd", "edit"},
	}

	// Extract the RunE body for a given command variable.
	// Looks for: var <varName> = &cobra.Command{ ... RunE: func(...) { <body> }, }
	extractRunE := func(varName string) string {
		// Find the var block for this command
		pat := regexp.MustCompile(`(?s)var\s+` + regexp.QuoteMeta(varName) + `\s*=\s*&cobra\.Command\{.*?RunE:\s*func\(.*?\)\s*error\s*\{(.*?)\}\s*[,}]`)
		m := pat.FindStringSubmatch(source)
		if m == nil {
			return ""
		}
		return m[1]
	}

	// --- dispatchCmd commands ---
	for _, tc := range dispatchCmdCommands {
		t.Run(tc.varName+"_uses_dispatchCmd", func(t *testing.T) {
			body := extractRunE(tc.varName)
			if body == "" {
				t.Fatalf("could not extract RunE body for %s", tc.varName)
			}
			if !strings.Contains(body, "dispatchCmd(") {
				t.Errorf("%s should call dispatchCmd, but RunE body is: %s", tc.varName, strings.TrimSpace(body))
			}
			// Must NOT use the wrong openers
			if strings.Contains(body, "dispatchCmdWithIndex(") {
				t.Errorf("%s should not call dispatchCmdWithIndex", tc.varName)
			}
			if strings.Contains(body, "dispatchCmdWithStdin(") {
				t.Errorf("%s should not call dispatchCmdWithStdin", tc.varName)
			}
			if strings.Contains(body, "index.OpenDB(") {
				t.Errorf("%s should not call index.OpenDB directly", tc.varName)
			}
		})
	}

	// --- dispatchCmdWithStdin commands ---
	for _, tc := range dispatchCmdWithStdinCommands {
		t.Run(tc.varName+"_uses_dispatchCmdWithStdin", func(t *testing.T) {
			body := extractRunE(tc.varName)
			if body == "" {
				t.Fatalf("could not extract RunE body for %s", tc.varName)
			}
			if !strings.Contains(body, "dispatchCmdWithStdin(") {
				t.Errorf("%s should call dispatchCmdWithStdin, but RunE body is: %s", tc.varName, strings.TrimSpace(body))
			}
			if strings.Contains(body, "dispatchCmdWithIndex(") {
				t.Errorf("%s should not call dispatchCmdWithIndex", tc.varName)
			}
			if strings.Contains(body, "index.OpenDB(") {
				t.Errorf("%s should not call index.OpenDB directly", tc.varName)
			}
		})
	}

	// --- setupCmd must be in setup.go, not using any shared dispatcher ---
	t.Run("setupCmd_in_setup_go", func(t *testing.T) {
		setupSrc, err := os.ReadFile("setup.go")
		if err != nil {
			t.Fatalf("reading setup.go: %v", err)
		}
		setupSource := string(setupSrc)

		if !strings.Contains(setupSource, "setupCmd") {
			t.Errorf("setupCmd should be defined in setup.go")
		}
		// setupCmd should NOT appear in commands.go RunE bodies (only in init() for AddCommand)
		if strings.Contains(source, "dispatchCmd(cmd, \"setup\"") ||
			strings.Contains(source, "dispatchCmdWithStdin(cmd, \"setup\"") ||
			strings.Contains(source, "dispatchCmdWithIndex(cmd, \"setup\"") {
			t.Errorf("setupCmd should not use shared dispatch functions in commands.go")
		}
	})

	// --- Completeness: every command registered in init() is tested ---
	t.Run("all_registered_commands_tested", func(t *testing.T) {
		// Extract all AddCommand calls
		addPat := regexp.MustCompile(`rootCmd\.AddCommand\((\w+)\)`)
		matches := addPat.FindAllStringSubmatch(source, -1)

		tested := make(map[string]bool)
		for _, tc := range dispatchCmdCommands {
			tested[tc.varName] = true
		}
		for _, tc := range dispatchCmdWithStdinCommands {
			tested[tc.varName] = true
		}
		tested["setupCmd"] = true
		tested["statusCmd"] = true
		tested["undoCmd"] = true
		tested["sessionCmd"] = true
		tested["benchCmd"] = true

		for _, m := range matches {
			cmdVar := m[1]
			if !tested[cmdVar] {
				t.Errorf("command %s is registered via AddCommand but not covered by opener alignment test — add it to the appropriate category", cmdVar)
			}
		}
	})
}
