// Package setup implements the `edr setup` command: index a repo and inject
// agent instructions into the appropriate config file.
package setup

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed instructions/*.md
var instructionFS embed.FS

// Target represents an agent platform to configure.
type Target string

const (
	TargetClaude  Target = "claude"
	TargetCursor  Target = "cursor"
	TargetCodex   Target = "codex"
	TargetGeneric Target = "generic"
)

// ConfigFile returns the filename for a given target.
func ConfigFile(t Target) string {
	switch t {
	case TargetClaude:
		return "CLAUDE.md"
	case TargetCursor:
		return ".cursorrules"
	case TargetCodex:
		return "AGENTS.md"
	default:
		return ""
	}
}

// Instructions returns the embedded instruction text for a target.
func Instructions(t Target) (string, error) {
	name := "instructions/" + string(t) + ".md"
	data, err := instructionFS.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("no instructions for target %q: %w", t, err)
	}
	return string(data), nil
}

// InjectInstructions appends the edr instruction block to the target config
// file. If the file already contains the edr marker, it returns without
// modifying the file.
func InjectInstructions(repoRoot string, target Target) (string, error) {
	text, err := Instructions(target)
	if err != nil {
		return "", err
	}

	if target == TargetGeneric {
		// Generic: just return the text (caller prints to stdout).
		return text, nil
	}

	filename := ConfigFile(target)
	path := filepath.Join(repoRoot, filename)

	// Check if already injected.
	existing, err := os.ReadFile(path)
	if err == nil && strings.Contains(string(existing), "edr: use for all file operations") {
		return path, fmt.Errorf("already configured: %s contains edr instructions", filename)
	}

	// Build content to append.
	var buf strings.Builder
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		buf.WriteString("\n")
	}
	if len(existing) > 0 {
		buf.WriteString("\n")
	}
	buf.WriteString(text)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", filename, err)
	}
	defer f.Close()

	if _, err := f.WriteString(buf.String()); err != nil {
		return "", fmt.Errorf("write %s: %w", filename, err)
	}

	return path, nil
}

// EnsureGitignore adds `.edr/` to .gitignore if not already present.
func EnsureGitignore(repoRoot string) error {
	path := filepath.Join(repoRoot, ".gitignore")
	existing, err := os.ReadFile(path)
	if err == nil {
		for _, line := range strings.Split(string(existing), "\n") {
			if strings.TrimSpace(line) == ".edr/" || strings.TrimSpace(line) == ".edr" {
				return nil // already present
			}
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open .gitignore: %w", err)
	}
	defer f.Close()

	prefix := ""
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		prefix = "\n"
	}
	if _, err := f.WriteString(prefix + ".edr/\n"); err != nil {
		return fmt.Errorf("write .gitignore: %w", err)
	}
	return nil
}

// DetectTarget looks at the repo root and suggests a target based on existing files.
func DetectTarget(repoRoot string) Target {
	// Check in order of specificity.
	if _, err := os.Stat(filepath.Join(repoRoot, "CLAUDE.md")); err == nil {
		return TargetClaude
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".cursorrules")); err == nil {
		return TargetCursor
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "AGENTS.md")); err == nil {
		return TargetCodex
	}
	return ""
}

// AllTargets returns the list of valid target names.
func AllTargets() []string {
	return []string{"claude", "cursor", "codex", "generic"}
}
