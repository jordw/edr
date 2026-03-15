// Package setup implements the `edr setup` command: index a repo and inject
// agent instructions into the appropriate config file.
package setup

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// InjectResult describes what happened during instruction injection.
type InjectResult struct {
	Path           string
	AlreadyCurrent bool   // instructions already present and hash matches
	Outdated       bool   // instructions present but hash differs
	InstalledHash  string // hash found in existing file (empty if none)
	Updated        bool   // old instructions replaced with new
}

// edrMarkers are strings we look for to detect existing edr instructions.
// Matches both old ("# edr: use for all file operations") and new ("# STOP. Use `edr`") headings.
var edrMarkers = []string{
	"Use `edr` for all file operations",
	"edr: use for all file operations",
}

// containsEdrMarker reports whether content contains any known edr marker.
func containsEdrMarker(content string) bool {
	for _, m := range edrMarkers {
		if strings.Contains(content, m) {
			return true
		}
	}
	return false
}

// hashMarkerPrefix is the HTML comment that stamps the build hash.
const hashMarkerPrefix = "<!-- edr-instructions hash:"

// hashMarkerRe extracts the hash from an existing marker comment.
var hashMarkerRe = regexp.MustCompile(`<!-- edr-instructions hash:(\S+) -->`)

// formatHashMarker returns the full marker comment for a given hash.
func formatHashMarker(hash string) string {
	return fmt.Sprintf("<!-- edr-instructions hash:%s -->", hash)
}

// extractInstalledHash returns the hash from an existing edr block, or "".
func extractInstalledHash(content string) string {
	m := hashMarkerRe.FindStringSubmatch(content)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

// InjectInstructions appends the edr instruction block to the target config
// file. buildHash is stamped into the block so future runs can detect staleness.
// If the file already contains current instructions (same hash), it reports
// AlreadyCurrent. If the hash differs, it reports Outdated (use force to replace).
func InjectInstructions(repoRoot string, target Target, buildHash string, force bool) (InjectResult, error) {
	text, err := Instructions(target)
	if err != nil {
		return InjectResult{}, err
	}

	if target == TargetGeneric {
		// Generic: just return the text (caller prints to stdout).
		return InjectResult{Path: text}, nil
	}

	filename := ConfigFile(target)
	path := filepath.Join(repoRoot, filename)

	existing, _ := os.ReadFile(path)
	content := string(existing)
	hasMarker := containsEdrMarker(content)
	installedHash := extractInstalledHash(content)

	// Same hash → already current, nothing to do.
	if hasMarker && installedHash == buildHash && !force {
		return InjectResult{Path: path, AlreadyCurrent: true, InstalledHash: installedHash}, nil
	}

	// Different hash (or no hash) but marker present → outdated.
	if hasMarker && !force {
		return InjectResult{Path: path, Outdated: true, InstalledHash: installedHash}, nil
	}

	// Strip old block if present (force or first inject with stale content).
	if hasMarker {
		content = stripEdrBlock(content)
	}

	// Build new content: hash marker + instructions.
	var buf strings.Builder
	buf.WriteString(content)
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		buf.WriteString("\n")
	}
	if len(content) > 0 {
		buf.WriteString("\n")
	}
	buf.WriteString(formatHashMarker(buildHash))
	buf.WriteString("\n")
	buf.WriteString(text)

	if err := os.WriteFile(path, []byte(buf.String()), 0644); err != nil {
		return InjectResult{}, fmt.Errorf("write %s: %w", filename, err)
	}

	result := InjectResult{Path: path}
	if hasMarker {
		result.Updated = true
	}
	return result, nil
}

// stripEdrBlock removes the edr instruction block from content.
// The block starts at the hash marker comment (<!-- edr-instructions hash:... -->)
// or the heading containing an edr marker, and extends to the next top-level heading
// that isn't part of edr, or end of file.
func stripEdrBlock(content string) string {
	lines := strings.Split(content, "\n")
	var out []string
	inBlock := false
	for _, line := range lines {
		if !inBlock && (strings.HasPrefix(line, hashMarkerPrefix) || containsEdrMarker(line)) {
			inBlock = true
			continue
		}
		if inBlock {
			// End block at next top-level heading (# ...) that isn't part of edr.
			if strings.HasPrefix(line, "# ") && !strings.Contains(line, "edr") {
				inBlock = false
				out = append(out, line)
			}
			continue
		}
		out = append(out, line)
	}
	// Trim trailing blank lines left by removal.
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	if len(out) > 0 {
		return strings.Join(out, "\n") + "\n"
	}
	return ""
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
	if _, err := os.Stat(filepath.Join(repoRoot, ".claude")); err == nil {
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
