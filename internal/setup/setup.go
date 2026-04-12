// Package setup implements the `edr setup` command: index a repo and inject
// agent instructions into global config files.
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
const (
	TargetClaude  Target = "claude"
	TargetCodex   Target = "codex"
	TargetCursor  Target = "cursor"
	TargetGeneric Target = "generic"
)

// GlobalTargets returns the targets that support global installation.
func GlobalTargets() []Target {
	return []Target{TargetClaude, TargetCodex, TargetCursor}
}

// ownsFile reports whether edr owns the entire config file for a target.
// When true, InjectGlobal overwrites the file instead of appending a block.
func ownsFile(t Target) bool {
	return t == TargetCursor
}

// GlobalConfigPath returns the full path for a target's global config file.
func GlobalConfigPath(t Target) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	switch t {
	case TargetClaude:
		return filepath.Join(home, ".claude", "CLAUDE.md"), nil
	case TargetCodex:
		return filepath.Join(home, ".codex", "AGENTS.md"), nil
	case TargetCursor:
		return filepath.Join(home, ".cursor", "rules", "edr.mdc"), nil
	default:
		return "", fmt.Errorf("no global config path for target %q", t)
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
	Path           string `json:"path"`
	Target         string `json:"target"`
	AlreadyCurrent bool   `json:"already_current,omitempty"`
	Outdated       bool   `json:"outdated,omitempty"`
	InstalledHash  string `json:"installed_hash,omitempty"`
	Updated        bool   `json:"updated,omitempty"`
	Created        bool   `json:"created,omitempty"`
	Removed        bool   `json:"removed,omitempty"`
	Error          string `json:"error,omitempty"`
}

// Block delimiters — the opening sentinel includes the build hash,
// the closing sentinel is fixed. Together they allow surgical updates.
const (
	blockOpen  = "<!-- edr-instructions hash:"
	blockClose = "<!-- /edr-instructions -->"
)

// hashMarkerRe extracts the hash from an existing opening sentinel.
var hashMarkerRe = regexp.MustCompile(`<!-- edr-instructions hash:(\S+) -->`)

// formatOpenSentinel returns the opening sentinel for a given hash.
func formatOpenSentinel(hash string) string {
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

// containsEdrBlock reports whether content contains a sentinel-delimited edr block.
func containsEdrBlock(content string) bool {
	return strings.Contains(content, blockOpen)
}

// stripEdrBlock removes the sentinel-delimited edr instruction block from content.
// Falls back to legacy marker detection for pre-sentinel files.
func stripEdrBlock(content string) string {
	lines := strings.Split(content, "\n")
	var out []string
	inBlock := false
	for _, line := range lines {
		if !inBlock && strings.HasPrefix(line, blockOpen) {
			inBlock = true
			continue
		}
		if inBlock {
			if strings.TrimSpace(line) == blockClose {
				inBlock = false
				continue
			}
			continue
		}
		// Legacy detection: heading-based markers (pre-sentinel files).
		if !inBlock && containsLegacyMarker(line) {
			inBlock = true
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

// Legacy markers for pre-sentinel files.
var legacyMarkers = []string{
	"use `edr` for all file operations",
	"Use `edr` instead of",
	"edr: use for all file operations",
}

// containsLegacyMarker reports whether a line contains an old-style edr marker.
func containsLegacyMarker(line string) bool {
	for _, m := range legacyMarkers {
		if strings.Contains(line, m) {
			return true
		}
	}
	return false
}

// cursorMDC wraps instruction text in Cursor's .mdc format with frontmatter.
func cursorMDC(text, hash string) string {
	var buf strings.Builder
	buf.WriteString("---\n")
	buf.WriteString("description: edr — use for all file operations\n")
	buf.WriteString("alwaysApply: true\n")
	buf.WriteString("---\n\n")
	buf.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		buf.WriteString("\n")
	}
	buf.WriteString(formatOpenSentinel(hash))
	buf.WriteString("\n")
	buf.WriteString(blockClose)
	buf.WriteString("\n")
	return buf.String()
}

// InjectGlobal writes edr instructions into a single global config file.
// The block is wrapped in sentinel comments for surgical future updates.
func InjectGlobal(target Target, buildHash string, force bool) (InjectResult, error) {
	path, err := GlobalConfigPath(target)
	if err != nil {
		return InjectResult{}, err
	}

	text, err := Instructions(target)
	if err != nil {
		return InjectResult{}, err
	}

	result := InjectResult{Path: path, Target: string(target)}

	existing, _ := os.ReadFile(path)
	content := string(existing)
	hasBlock := containsEdrBlock(content) || containsLegacyMarkerInContent(content)
	installedHash := extractInstalledHash(content)

	// Same hash → already current.
	if hasBlock && installedHash == buildHash && !force {
		result.AlreadyCurrent = true
		result.InstalledHash = installedHash
		return result, nil
	}

	// Different hash but block present → outdated (unless force).
	if hasBlock && !force {
		result.Outdated = true
		result.InstalledHash = installedHash
		return result, nil
	}

	// Ensure parent directory exists.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return InjectResult{}, fmt.Errorf("create directory %s: %w", dir, err)
	}

	// For targets where edr owns the entire file, overwrite it.
	if ownsFile(target) {
		fileContent := cursorMDC(text, buildHash)
		if err := os.WriteFile(path, []byte(fileContent), 0644); err != nil {
			return InjectResult{}, fmt.Errorf("write %s: %w", path, err)
		}
		if hasBlock {
			result.Updated = true
		} else {
			result.Created = true
		}
		return result, nil
	}

	// For shared config files, strip old block and append new one.
	if hasBlock {
		content = stripEdrBlock(content)
	}

	// Build new block with sentinels.
	var block strings.Builder
	block.WriteString(formatOpenSentinel(buildHash))
	block.WriteString("\n")
	block.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		block.WriteString("\n")
	}
	block.WriteString(blockClose)
	block.WriteString("\n")

	// Append to existing content.
	var buf strings.Builder
	buf.WriteString(content)
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		buf.WriteString("\n")
	}
	if len(content) > 0 {
		buf.WriteString("\n")
	}
	buf.WriteString(block.String())

	if err := os.WriteFile(path, []byte(buf.String()), 0644); err != nil {
		return InjectResult{}, fmt.Errorf("write %s: %w", path, err)
	}

	if hasBlock {
		result.Updated = true
	} else {
		result.Created = true
	}
	return result, nil
}

// InjectAllGlobal writes edr instructions to all global targets.
func InjectAllGlobal(buildHash string, force bool) ([]InjectResult, error) {
	var results []InjectResult
	for _, t := range GlobalTargets() {
		r, err := InjectGlobal(t, buildHash, force)
		if err != nil {
			r = InjectResult{Target: string(t), Error: err.Error()}
		}
		results = append(results, r)
	}
	return results, nil
}

// UninstallGlobal removes edr instructions from a single global config file.
func UninstallGlobal(target Target) (InjectResult, error) {
	path, err := GlobalConfigPath(target)
	if err != nil {
		return InjectResult{}, err
	}

	result := InjectResult{Path: path, Target: string(target)}

	existing, err := os.ReadFile(path)
	if err != nil {
		// File doesn't exist — nothing to remove.
		return result, nil
	}
	content := string(existing)
	hasBlock := containsEdrBlock(content) || containsLegacyMarkerInContent(content)
	if !hasBlock {
		return result, nil
	}

	// For owned files, just delete the file.
	if ownsFile(target) {
		if err := os.Remove(path); err != nil {
			return InjectResult{}, fmt.Errorf("remove %s: %w", path, err)
		}
		result.Removed = true
		return result, nil
	}

	// For shared config files, strip the edr block.
	cleaned := stripEdrBlock(content)
	if err := os.WriteFile(path, []byte(cleaned), 0644); err != nil {
		return InjectResult{}, fmt.Errorf("write %s: %w", path, err)
	}
	result.Removed = true
	return result, nil
}

// UninstallAllGlobal removes edr instructions from all global targets.
func UninstallAllGlobal() ([]InjectResult, error) {
	var results []InjectResult
	for _, t := range GlobalTargets() {
		r, err := UninstallGlobal(t)
		if err != nil {
			r = InjectResult{Target: string(t), Error: err.Error()}
		}
		results = append(results, r)
	}
	// Remove sentinel so auto-update doesn't re-install.
	path, err := sentinelPath()
	if err == nil {
		os.Remove(path)
	}
	return results, nil
}

// GlobalStatus checks the current state of global instructions without modifying anything.
func GlobalStatus(buildHash string) []InjectResult {
	var results []InjectResult
	for _, t := range GlobalTargets() {
		path, err := GlobalConfigPath(t)
		if err != nil {
			results = append(results, InjectResult{Target: string(t), Error: err.Error()})
			continue
		}
		r := InjectResult{Path: path, Target: string(t)}
		existing, err := os.ReadFile(path)
		if err != nil {
			// File doesn't exist — not installed.
			results = append(results, r)
			continue
		}
		content := string(existing)
		hasBlock := containsEdrBlock(content) || containsLegacyMarkerInContent(content)
		if !hasBlock {
			results = append(results, r)
			continue
		}
		installedHash := extractInstalledHash(content)
		r.InstalledHash = installedHash
		if installedHash == buildHash {
			r.AlreadyCurrent = true
		} else {
			r.Outdated = true
		}
		results = append(results, r)
	}
	return results
}

// containsLegacyMarkerInContent checks if any line in content has a legacy marker.
func containsLegacyMarkerInContent(content string) bool {
	for _, m := range legacyMarkers {
		if strings.Contains(content, m) {
			return true
		}
	}
	return false
}

// AllTargets returns the list of valid target names.
func AllTargets() []string {
	return []string{"claude", "codex", "cursor", "generic"}
}

// sentinelPath returns ~/.edr/global_hash.
func sentinelPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".edr", "global_hash"), nil
}

// WriteSentinel records that global instructions were installed for the given hash.
func WriteSentinel(buildHash string) error {
	path, err := sentinelPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(buildHash+"\n"), 0644)
}

// ReadSentinel returns the hash from the sentinel file, or "" if not found.
func ReadSentinel() string {
	path, err := sentinelPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// AutoUpdate checks the sentinel and silently updates global instructions if stale.
// Returns true if an update was performed. Errors are swallowed — this is best-effort.
func AutoUpdate(buildHash string) bool {
	if buildHash == "" || buildHash == "unknown" {
		return false
	}
	installed := ReadSentinel()
	if installed == buildHash {
		return false
	}
	if installed == "" {
		// No sentinel = never opted in via setup. Don't auto-install.
		return false
	}
	// Sentinel exists but hash differs — they opted in, edr was updated.
	_, _ = InjectAllGlobal(buildHash, true)
	_ = WriteSentinel(buildHash)
	return true
}
