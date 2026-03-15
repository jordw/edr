package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstructionsEmbed(t *testing.T) {
	for _, target := range AllTargets() {
		t.Run(target, func(t *testing.T) {
			text, err := Instructions(Target(target))
			if err != nil {
				t.Fatalf("Instructions(%q): %v", target, err)
			}
			if len(text) < 100 {
				t.Errorf("Instructions(%q) too short: %d bytes", target, len(text))
			}
			if text[0] != '#' {
				t.Errorf("Instructions(%q) should start with markdown heading", target)
			}
		})
	}
}

func TestInjectInstructions(t *testing.T) {
	dir := t.TempDir()

	// Inject into new file.
	path, err := InjectInstructions(dir, TargetClaude)
	if err != nil {
		t.Fatalf("InjectInstructions: %v", err)
	}
	if filepath.Base(path) != "CLAUDE.md" {
		t.Errorf("expected CLAUDE.md, got %s", path)
	}
	data, _ := os.ReadFile(path)
	if len(data) < 100 {
		t.Errorf("CLAUDE.md too short: %d bytes", len(data))
	}

	// Second call should be idempotent.
	_, err = InjectInstructions(dir, TargetClaude)
	if err == nil {
		t.Error("expected error on duplicate injection")
	}
}

func TestInjectIntoExistingFile(t *testing.T) {
	dir := t.TempDir()
	existing := "# My Project\n\nSome existing instructions.\n"
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(existing), 0644)

	path, err := InjectInstructions(dir, TargetClaude)
	if err != nil {
		t.Fatalf("InjectInstructions: %v", err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	// Should preserve existing content.
	if content[:len(existing)] != existing {
		t.Error("existing content was not preserved")
	}
	// Should have edr instructions appended.
	if len(content) <= len(existing) {
		t.Error("instructions were not appended")
	}
}

func TestEnsureGitignore(t *testing.T) {
	dir := t.TempDir()

	// Creates .gitignore if missing.
	if err := EnsureGitignore(dir); err != nil {
		t.Fatalf("EnsureGitignore: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(data) != ".edr/\n" {
		t.Errorf("expected '.edr/\\n', got %q", string(data))
	}

	// Idempotent.
	if err := EnsureGitignore(dir); err != nil {
		t.Fatalf("EnsureGitignore (2nd): %v", err)
	}
	data, _ = os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(data) != ".edr/\n" {
		t.Errorf("should not duplicate: got %q", string(data))
	}
}

func TestDetectTarget(t *testing.T) {
	dir := t.TempDir()

	// No files: empty.
	if got := DetectTarget(dir); got != "" {
		t.Errorf("expected empty, got %q", got)
	}

	// Create .cursorrules.
	os.WriteFile(filepath.Join(dir, ".cursorrules"), []byte("x"), 0644)
	if got := DetectTarget(dir); got != TargetCursor {
		t.Errorf("expected cursor, got %q", got)
	}

	// CLAUDE.md takes priority.
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("x"), 0644)
	if got := DetectTarget(dir); got != TargetClaude {
		t.Errorf("expected claude, got %q", got)
	}
}

func TestConfigFile(t *testing.T) {
	cases := []struct {
		target Target
		want   string
	}{
		{TargetClaude, "CLAUDE.md"},
		{TargetCursor, ".cursorrules"},
		{TargetCodex, "AGENTS.md"},
		{TargetGeneric, ""},
	}
	for _, tc := range cases {
		if got := ConfigFile(tc.target); got != tc.want {
			t.Errorf("ConfigFile(%q) = %q, want %q", tc.target, got, tc.want)
		}
	}
}
