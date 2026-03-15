package setup

import (
	"os"
	"path/filepath"
	"strings"
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

func TestInstructionsNoBodyFlag(t *testing.T) {
	for _, target := range AllTargets() {
		t.Run(target, func(t *testing.T) {
			text, err := Instructions(Target(target))
			if err != nil {
				t.Fatalf("Instructions(%q): %v", target, err)
			}
			if strings.Contains(text, "--body") {
				t.Errorf("Instructions(%q) contains non-existent --body flag", target)
			}
		})
	}
}

func TestInstructionsSessionOneLiner(t *testing.T) {
	for _, target := range AllTargets() {
		t.Run(target, func(t *testing.T) {
			text, err := Instructions(Target(target))
			if err != nil {
				t.Fatalf("Instructions(%q): %v", target, err)
			}
			if strings.Contains(text, "uuidgen") {
				t.Errorf("Instructions(%q) still uses uuidgen (should use edr session-id)", target)
			}
			if strings.Contains(text, "```\nexport EDR_SESSION") {
				t.Errorf("Instructions(%q) has session setup as code block (should be inline)", target)
			}
		})
	}
}

func TestInjectInstructions(t *testing.T) {
	dir := t.TempDir()

	// Inject into new file.
	ir, err := InjectInstructions(dir, TargetClaude, "abc1234", false)
	if err != nil {
		t.Fatalf("InjectInstructions: %v", err)
	}
	if filepath.Base(ir.Path) != "CLAUDE.md" {
		t.Errorf("expected CLAUDE.md, got %s", ir.Path)
	}
	data, _ := os.ReadFile(ir.Path)
	if len(data) < 100 {
		t.Errorf("CLAUDE.md too short: %d bytes", len(data))
	}
	// Should contain the hash marker.
	if !strings.Contains(string(data), "<!-- edr-instructions hash:abc1234 -->") {
		t.Error("missing hash marker in injected content")
	}

	// Second call with same hash should report already current.
	ir2, err := InjectInstructions(dir, TargetClaude, "abc1234", false)
	if err != nil {
		t.Fatalf("second InjectInstructions should not error: %v", err)
	}
	if !ir2.AlreadyCurrent {
		t.Error("expected AlreadyCurrent=true on duplicate injection with same hash")
	}
}

func TestInjectInstructionsOutdated(t *testing.T) {
	dir := t.TempDir()

	// Inject with hash "old1234".
	_, err := InjectInstructions(dir, TargetClaude, "old1234", false)
	if err != nil {
		t.Fatalf("InjectInstructions: %v", err)
	}

	// Call with different hash (no force) should report outdated.
	ir, err := InjectInstructions(dir, TargetClaude, "new5678", false)
	if err != nil {
		t.Fatalf("InjectInstructions: %v", err)
	}
	if !ir.Outdated {
		t.Error("expected Outdated=true when hash differs")
	}
	if ir.InstalledHash != "old1234" {
		t.Errorf("expected InstalledHash=old1234, got %q", ir.InstalledHash)
	}
	if ir.AlreadyCurrent {
		t.Error("should not be AlreadyCurrent when hash differs")
	}
}

func TestInjectInstructionsNoHash(t *testing.T) {
	dir := t.TempDir()

	// Simulate pre-versioning file: has the marker but no hash comment.
	old := "# My Project\n\n# edr: use for all file operations\n\nOld instructions.\n"
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(old), 0644)

	// Should detect as outdated (marker present, hash missing).
	ir, err := InjectInstructions(dir, TargetClaude, "abc1234", false)
	if err != nil {
		t.Fatalf("InjectInstructions: %v", err)
	}
	if !ir.Outdated {
		t.Error("expected Outdated=true for pre-versioning file")
	}
	if ir.InstalledHash != "" {
		t.Errorf("expected empty InstalledHash, got %q", ir.InstalledHash)
	}
}

func TestInjectInstructionsForce(t *testing.T) {
	dir := t.TempDir()

	// Inject with old hash.
	_, err := InjectInstructions(dir, TargetClaude, "old1234", false)
	if err != nil {
		t.Fatalf("InjectInstructions: %v", err)
	}

	// Force re-inject with new hash.
	ir, err := InjectInstructions(dir, TargetClaude, "new5678", true)
	if err != nil {
		t.Fatalf("InjectInstructions --force: %v", err)
	}
	if !ir.Updated {
		t.Error("expected Updated=true on force re-injection")
	}

	// Content should have exactly one edr block with new hash.
	data, _ := os.ReadFile(ir.Path)
	content := string(data)
	count := strings.Count(content, edrMarker)
	if count != 1 {
		t.Errorf("expected 1 edr marker after force, got %d", count)
	}
	if !strings.Contains(content, "hash:new5678") {
		t.Error("expected new hash in forced content")
	}
	if strings.Contains(content, "hash:old1234") {
		t.Error("old hash should be gone after force")
	}
}

func TestInjectIntoExistingFile(t *testing.T) {
	dir := t.TempDir()
	existing := "# My Project\n\nSome existing instructions.\n"
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(existing), 0644)

	ir, err := InjectInstructions(dir, TargetClaude, "abc1234", false)
	if err != nil {
		t.Fatalf("InjectInstructions: %v", err)
	}

	data, _ := os.ReadFile(ir.Path)
	content := string(data)

	// Should preserve existing content.
	if !strings.HasPrefix(content, existing) {
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

func TestStripEdrBlock(t *testing.T) {
	input := "# My Project\n\nSome content.\n\n# edr: use for all file operations\n\nEdr stuff here.\nMore edr stuff.\n\n## Sub heading\n\nMore edr.\n"
	got := stripEdrBlock(input)
	if strings.Contains(got, "edr: use for all file operations") {
		t.Errorf("stripEdrBlock did not remove edr block: %q", got)
	}
	if !strings.Contains(got, "# My Project") {
		t.Errorf("stripEdrBlock removed non-edr content: %q", got)
	}
}

func TestStripEdrBlockWithHashMarker(t *testing.T) {
	input := "# My Project\n\nSome content.\n\n<!-- edr-instructions hash:abc1234 -->\n# edr: use for all file operations\n\nEdr stuff here.\n"
	got := stripEdrBlock(input)
	if strings.Contains(got, "edr-instructions") {
		t.Errorf("stripEdrBlock did not remove hash marker: %q", got)
	}
	if strings.Contains(got, "edr: use for all file operations") {
		t.Errorf("stripEdrBlock did not remove edr block: %q", got)
	}
	if !strings.Contains(got, "# My Project") {
		t.Errorf("stripEdrBlock removed non-edr content: %q", got)
	}
}

func TestExtractInstalledHash(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"with hash", "<!-- edr-instructions hash:abc1234 -->\n# edr: use", "abc1234"},
		{"no hash", "# edr: use for all file operations\n\nstuff", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractInstalledHash(tc.content); got != tc.want {
				t.Errorf("extractInstalledHash() = %q, want %q", got, tc.want)
			}
		})
	}
}
