package cmdspec

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestPublicCommandSurface asserts that exactly the expected commands are public.
func TestPublicCommandSurface(t *testing.T) {
	// These are the commands that should appear in edr --help
	expected := map[string]bool{
		"read": true, "search": true, "map": true,
		"edit": true, "write": true, "refs": true,
		"verify": true, "reindex": true, "rename": true,
		"run": true, "session": true, "setup": true, "next": true,
	}

	for _, s := range Registry {
		if s.Internal {
			if expected[s.Name] {
				t.Errorf("%s is marked internal but expected to be public", s.Name)
			}
			continue
		}
		if !expected[s.Name] {
			t.Errorf("%s is public but not in expected set (add to expected or mark internal)", s.Name)
		}
	}

	for name := range expected {
		if s := ByName(name); s == nil {
			t.Errorf("expected public command %q not found in registry", name)
		}
	}
}

// TestCLAUDEmdCommandsMatchRegistry validates that commands documented in
// CLAUDE.md are backed by cmdspec entries.
func TestCLAUDEmdCommandsMatchRegistry(t *testing.T) {
	data, err := os.ReadFile("../../CLAUDE.md")
	if err != nil {
		t.Skip("CLAUDE.md not found at expected path")
	}
	content := string(data)

	// Extract command names from ### `command` — headers
	headerRe := regexp.MustCompile("(?m)^### `(\\w+)` —")
	matches := headerRe.FindAllStringSubmatch(content, -1)

	for _, m := range matches {
		cmd := m[1]
		if s := ByName(cmd); s == nil {
			t.Errorf("CLAUDE.md documents command %q but it's not in cmdspec registry", cmd)
		}
	}
}

// TestCLAUDEmdFlagsMatchRegistry validates that flags documented in
// CLAUDE.md exist in the cmdspec registry for their respective commands.
func TestCLAUDEmdFlagsMatchRegistry(t *testing.T) {
	data, err := os.ReadFile("../../CLAUDE.md")
	if err != nil {
		t.Skip("CLAUDE.md not found at expected path")
	}
	content := string(data)

	// Map documented flags to commands based on section context
	// Look for patterns like --flag-name in code blocks
	flagRe := regexp.MustCompile(`--([a-zA-Z][\w-]*)`)
	flagMatches := flagRe.FindAllStringSubmatch(content, -1)

	// Build set of all known flags across all commands
	allFlags := make(map[string]bool)
	for _, s := range Registry {
		for _, f := range s.Flags {
			allFlags[f.Name] = true
			// Also add hyphenated form
			allFlags[strings.ReplaceAll(f.Name, "_", "-")] = true
		}
	}
	// Add well-known non-cmdspec flags
	allFlags["root"] = true
	allFlags["verbose"] = true
	allFlags["help"] = true
	allFlags["no-verify"] = true
	allFlags["no-body"] = true
	allFlags["no-group"] = true
	allFlags["read-after-edit"] = true
	allFlags["skip-index"] = true
	allFlags["json"] = true
	allFlags["cpuprofile"] = true
	allFlags["force"] = true // setup flag, not cmdspec
	allFlags["claude"] = true
	allFlags["cursor"] = true
	allFlags["codex"] = true
	allFlags["generic"] = true
	allFlags["new"] = true   // batch shorthand
	allFlags["old"] = true   // batch shorthand
	allFlags["sig"] = true   // batch shorthand
	allFlags["read"] = true  // batch flag
	allFlags["edit"] = true  // batch flag
	allFlags["write"] = true // batch flag
	allFlags["search"] = true // batch flag
	allFlags["verify"] = true // batch flag

	unknown := make(map[string]bool)
	for _, m := range flagMatches {
		flag := m[1]
		if !allFlags[flag] {
			unknown[flag] = true
		}
	}

	for flag := range unknown {
		t.Errorf("CLAUDE.md references flag --%s which is not in cmdspec or known flags", flag)
	}
}

// TestEveryFlagHasDescription ensures no flag has an empty description.
func TestEveryFlagHasDescription(t *testing.T) {
	for _, s := range Registry {
		for _, f := range s.Flags {
			if f.Desc == "" {
				t.Errorf("%s.%s: flag has empty description", s.Name, f.Name)
			}
		}
	}
}
