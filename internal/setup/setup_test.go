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
			if len(text) < 50 {
				t.Errorf("Instructions(%q) too short: %d bytes", target, len(text))
			}
			if !strings.Contains(text, "edr") {
				t.Errorf("Instructions(%q) should mention edr", target)
			}
		})
	}
}

func TestInjectGlobal(t *testing.T) {
	// Override home dir for testing.
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Inject into new file.
	r, err := InjectGlobal(TargetClaude, "abc1234", false)
	if err != nil {
		t.Fatalf("InjectGlobal: %v", err)
	}
	if !r.Created {
		t.Error("expected Created=true for new injection")
	}
	data, _ := os.ReadFile(r.Path)
	content := string(data)

	// Should have opening sentinel.
	if !strings.Contains(content, "<!-- edr-instructions hash:abc1234 -->") {
		t.Error("missing opening sentinel")
	}
	// Should have closing sentinel.
	if !strings.Contains(content, "<!-- /edr-instructions -->") {
		t.Error("missing closing sentinel")
	}

	// Second call with same hash → already current.
	r2, err := InjectGlobal(TargetClaude, "abc1234", false)
	if err != nil {
		t.Fatalf("second InjectGlobal: %v", err)
	}
	if !r2.AlreadyCurrent {
		t.Error("expected AlreadyCurrent=true on duplicate injection")
	}
}

func TestInjectGlobalOutdated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	_, err := InjectGlobal(TargetClaude, "old1234", false)
	if err != nil {
		t.Fatalf("InjectGlobal: %v", err)
	}

	r, err := InjectGlobal(TargetClaude, "new5678", false)
	if err != nil {
		t.Fatalf("InjectGlobal: %v", err)
	}
	if !r.Outdated {
		t.Error("expected Outdated=true when hash differs")
	}
	if r.InstalledHash != "old1234" {
		t.Errorf("expected InstalledHash=old1234, got %q", r.InstalledHash)
	}
}

func TestInjectGlobalForce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	r1, err := InjectGlobal(TargetClaude, "old1234", false)
	if err != nil {
		t.Fatalf("InjectGlobal: %v", err)
	}

	r2, err := InjectGlobal(TargetClaude, "new5678", true)
	if err != nil {
		t.Fatalf("InjectGlobal --force: %v", err)
	}
	if !r2.Updated {
		t.Error("expected Updated=true on force re-injection")
	}

	data, _ := os.ReadFile(r1.Path)
	content := string(data)

	// Should have exactly one block.
	if strings.Count(content, "<!-- edr-instructions hash:") != 1 {
		t.Error("expected exactly one opening sentinel after force")
	}
	if strings.Count(content, "<!-- /edr-instructions -->") != 1 {
		t.Error("expected exactly one closing sentinel after force")
	}
	if !strings.Contains(content, "hash:new5678") {
		t.Error("expected new hash")
	}
	if strings.Contains(content, "hash:old1234") {
		t.Error("old hash should be gone")
	}
}

func TestInjectGlobalPreservesExisting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create existing content in the file.
	dir := filepath.Join(home, ".claude")
	os.MkdirAll(dir, 0755)
	existing := "# My global Claude instructions\n\nDo not use emojis.\n"
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(existing), 0644)

	r, err := InjectGlobal(TargetClaude, "abc1234", false)
	if err != nil {
		t.Fatalf("InjectGlobal: %v", err)
	}

	data, _ := os.ReadFile(r.Path)
	content := string(data)

	if !strings.HasPrefix(content, existing) {
		t.Error("existing content not preserved")
	}
	if !strings.Contains(content, "<!-- edr-instructions hash:abc1234 -->") {
		t.Error("edr block not appended")
	}
}

func TestInjectAllGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	results, err := InjectAllGlobal("abc1234", false)
	if err != nil {
		t.Fatalf("InjectAllGlobal: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Error != "" {
			t.Errorf("target %s: unexpected error: %s", r.Target, r.Error)
		}
		if !r.Created {
			t.Errorf("target %s: expected Created=true", r.Target)
		}
	}

	// Verify files exist.
	if _, err := os.Stat(filepath.Join(home, ".claude", "CLAUDE.md")); err != nil {
		t.Error("~/.claude/CLAUDE.md not created")
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "AGENTS.md")); err != nil {
		t.Error("~/.codex/AGENTS.md not created")
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor", "rules", "edr.mdc")); err != nil {
		t.Error("~/.cursor/rules/edr.mdc not created")
	}
}

func TestGlobalStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Nothing installed.
	status := GlobalStatus("abc1234")
	for _, s := range status {
		if s.AlreadyCurrent || s.Outdated {
			t.Errorf("target %s: expected no status on empty", s.Target)
		}
	}

	// Install.
	InjectAllGlobal("abc1234", false)

	// Now should be current.
	status = GlobalStatus("abc1234")
	for _, s := range status {
		if !s.AlreadyCurrent {
			t.Errorf("target %s: expected AlreadyCurrent", s.Target)
		}
	}

	// Different hash → outdated.
	status = GlobalStatus("new5678")
	for _, s := range status {
		if !s.Outdated {
			t.Errorf("target %s: expected Outdated", s.Target)
		}
	}
}

func TestStripEdrBlock(t *testing.T) {
	input := "# My stuff\n\nSome content.\n\n<!-- edr-instructions hash:abc1234 -->\n# edr: use for all file operations\n\nEdr stuff here.\n<!-- /edr-instructions -->\n"
	got := stripEdrBlock(input)
	if strings.Contains(got, "edr-instructions") {
		t.Errorf("block not stripped: %q", got)
	}
	if strings.Contains(got, "edr: use for all file operations") {
		t.Errorf("edr content not stripped: %q", got)
	}
	if !strings.Contains(got, "# My stuff") {
		t.Errorf("non-edr content removed: %q", got)
	}
}

func TestStripEdrBlockLegacy(t *testing.T) {
	// Legacy format: no sentinels, just heading-based marker.
	input := "# My stuff\n\n# edr: use for all file operations\n\nEdr stuff.\n"
	got := stripEdrBlock(input)
	if strings.Contains(got, "edr: use for all file operations") {
		t.Errorf("legacy block not stripped: %q", got)
	}
	if !strings.Contains(got, "# My stuff") {
		t.Errorf("non-edr content removed: %q", got)
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

func TestSentinelRoundtrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// No sentinel initially.
	if got := ReadSentinel(); got != "" {
		t.Errorf("expected empty sentinel, got %q", got)
	}

	// Write and read back.
	if err := WriteSentinel("abc1234"); err != nil {
		t.Fatalf("WriteSentinel: %v", err)
	}
	if got := ReadSentinel(); got != "abc1234" {
		t.Errorf("expected abc1234, got %q", got)
	}

	// Overwrite.
	WriteSentinel("new5678")
	if got := ReadSentinel(); got != "new5678" {
		t.Errorf("expected new5678, got %q", got)
	}
}

func TestAutoUpdateNoSentinel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// No sentinel = never opted in. Should not auto-install.
	if AutoUpdate("abc1234") {
		t.Error("AutoUpdate should return false when no sentinel exists")
	}
	// Verify nothing was written.
	if _, err := os.Stat(filepath.Join(home, ".claude", "CLAUDE.md")); err == nil {
		t.Error("should not have created global instructions without opt-in")
	}
}

func TestAutoUpdateSameHash(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	WriteSentinel("abc1234")
	if AutoUpdate("abc1234") {
		t.Error("AutoUpdate should return false when hash matches")
	}
}

func TestAutoUpdateStaleHash(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Simulate prior opt-in.
	InjectAllGlobal("old1234", false)
	WriteSentinel("old1234")

	// Now edr was rebuilt with new hash.
	if !AutoUpdate("new5678") {
		t.Error("AutoUpdate should return true when hash differs")
	}

	// Verify instructions were updated.
	data, _ := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
	if !strings.Contains(string(data), "hash:new5678") {
		t.Error("global instructions not updated to new hash")
	}

	// Sentinel should be updated too.
	if got := ReadSentinel(); got != "new5678" {
		t.Errorf("sentinel not updated, got %q", got)
	}
}

func TestAllTargets(t *testing.T) {
	targets := AllTargets()
	if len(targets) != 4 {
		t.Errorf("expected 4 targets, got %d: %v", len(targets), targets)
	}
	for _, target := range targets {
		_, err := Instructions(Target(target))
		if err != nil {
			t.Errorf("Instructions(%q) failed: %v", target, err)
		}
	}
}

func TestCursorInject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	r, err := InjectGlobal(TargetCursor, "abc1234", false)
	if err != nil {
		t.Fatalf("InjectGlobal(cursor): %v", err)
	}
	if !r.Created {
		t.Error("expected Created=true")
	}

	data, _ := os.ReadFile(r.Path)
	content := string(data)

	// Should have MDC frontmatter at the start.
	if !strings.HasPrefix(content, "---\n") {
		t.Error("cursor file should start with frontmatter")
	}
	if !strings.Contains(content, "alwaysApply: true") {
		t.Error("cursor file should have alwaysApply frontmatter")
	}
	// Should have hash sentinel at the end.
	if !strings.Contains(content, "hash:abc1234") {
		t.Error("cursor file should contain hash sentinel")
	}

	// Force update with new hash.
	r2, err := InjectGlobal(TargetCursor, "new5678", true)
	if err != nil {
		t.Fatalf("InjectGlobal(cursor) force: %v", err)
	}
	if !r2.Updated {
		t.Error("expected Updated=true on force")
	}

	data, _ = os.ReadFile(r.Path)
	content = string(data)
	if !strings.Contains(content, "hash:new5678") {
		t.Error("cursor file should have new hash")
	}
	if strings.Contains(content, "hash:abc1234") {
		t.Error("cursor file should not have old hash")
	}
}

func TestUninstallGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Install first.
	InjectAllGlobal("abc1234", false)

	// Uninstall Claude (shared file — block removed, file preserved).
	r, err := UninstallGlobal(TargetClaude)
	if err != nil {
		t.Fatalf("UninstallGlobal(claude): %v", err)
	}
	if !r.Removed {
		t.Error("expected Removed=true")
	}
	data, _ := os.ReadFile(r.Path)
	if strings.Contains(string(data), "edr-instructions") {
		t.Error("edr block should be removed from claude file")
	}

	// Uninstall Cursor (owned file — file deleted).
	r2, err := UninstallGlobal(TargetCursor)
	if err != nil {
		t.Fatalf("UninstallGlobal(cursor): %v", err)
	}
	if !r2.Removed {
		t.Error("expected Removed=true for cursor")
	}
	if _, err := os.Stat(r2.Path); err == nil {
		t.Error("cursor file should be deleted")
	}
}

func TestUninstallAllGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Install + write sentinel.
	InjectAllGlobal("abc1234", false)
	WriteSentinel("abc1234")

	results, err := UninstallAllGlobal()
	if err != nil {
		t.Fatalf("UninstallAllGlobal: %v", err)
	}
	for _, r := range results {
		if !r.Removed {
			t.Errorf("target %s: expected Removed=true", r.Target)
		}
	}

	// Sentinel should be removed.
	if got := ReadSentinel(); got != "" {
		t.Errorf("sentinel should be cleared, got %q", got)
	}
}

func TestUninstallNotInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Uninstall when nothing is installed — should not error.
	r, err := UninstallGlobal(TargetClaude)
	if err != nil {
		t.Fatalf("UninstallGlobal: %v", err)
	}
	if r.Removed {
		t.Error("Removed should be false when nothing installed")
	}
}
