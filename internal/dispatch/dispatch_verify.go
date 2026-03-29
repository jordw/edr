package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jordw/edr/internal/index"
)

// DispatchVerify runs verification without requiring a DB or index.
// Runs verification without requiring a symbol store.
func DispatchVerify(ctx context.Context, root string, args []string, flags map[string]any) (any, error) {
	return runVerify(ctx, nil, root, args, flags)
}

func runVerify(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	command := flagString(flags, "command", "")
	level := flagString(flags, "level", "build")
	if flagBool(flags, "test", false) {
		level = "test"
	}
	if command == "" {
		// Check config.json for persistent verify command
		edrDir := index.HomeEdrDir(root)
		command = loadVerifyConfig(edrDir, level)
	}
	if command == "" {
		// Auto-detect based on project files
		if _, err := os.Stat(root + "/go.mod"); err == nil {
			scope := goVerifyScope(root, flags)
			if level == "test" {
				command = "go test " + scope
			} else {
				command = "go build " + scope
			}
		} else if _, err := os.Stat(root + "/package.json"); err == nil {
			if level == "test" {
				command = "npm test"
			} else {
				command = "npx tsc --noEmit"
			}
		} else if _, err := os.Stat(root + "/Cargo.toml"); err == nil {
			if level == "test" {
				command = "cargo test"
			} else {
				command = "cargo check"
			}
		} else if cmd := detectMakefile(root, level); cmd != "" {
			command = cmd
		} else {
			return map[string]any{
				"status": "skipped",
				"reason": "no command specified and could not auto-detect project type",
			}, nil
		}
	}

	timeout := flagInt(flags, "timeout", 120)
	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(cmdCtx, "sh", "-c", command)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	durationMs := time.Since(start).Milliseconds()

	result := map[string]any{
		"command":     command,
		"duration_ms": durationMs,
	}

	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			result["status"] = "failed"
			result["error"] = fmt.Sprintf("timeout after %ds", timeout)
		} else {
			result["status"] = "failed"
			result["error"] = err.Error()
		}
		// Include output tail so agents can see compiler errors / failing tests
		result["output"] = verifyOutputTail(string(out), 40)
	} else {
		result["status"] = "passed"
	}

	return result, nil
}

// verifyOutputTail returns the last N lines of output, trimmed.
// On success we omit output entirely; on failure this surfaces diagnostics.
func verifyOutputTail(output string, maxLines int) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	lines := strings.Split(output, "\n")
	if len(lines) <= maxLines {
		return output
	}
	return fmt.Sprintf("... (%d lines truncated)\n%s", len(lines)-maxLines,
		strings.Join(lines[len(lines)-maxLines:], "\n"))
}

// detectMakefile checks for a Makefile and probes for test/check targets.
// Returns a make command or "" if no Makefile found.
// loadVerifyConfig reads config.json from the edr data directory.
// Supports {"verify": "cmd"} or {"verify": {"build": "cmd", "test": "cmd"}}.
func loadVerifyConfig(edrDir, level string) string {
	data, err := os.ReadFile(filepath.Join(edrDir, "config.json"))
	if err != nil {
		return ""
	}
	var cfg map[string]any
	if json.Unmarshal(data, &cfg) != nil {
		return ""
	}
	v, ok := cfg["verify"]
	if !ok {
		return ""
	}
	// Simple string: one command for all levels
	if s, ok := v.(string); ok {
		return s
	}
	// Object with level keys
	if m, ok := v.(map[string]any); ok {
		if cmd, ok := m[level].(string); ok {
			return cmd
		}
		// Fall back to "build" if level not found
		if cmd, ok := m["build"].(string); ok {
			return cmd
		}
	}
	return ""
}

func detectMakefile(root, level string) string {
	if _, err := os.Stat(filepath.Join(root, "Makefile")); err != nil {
		return ""
	}
	if level == "test" {
		// Probe for common test targets: test, check
		for _, target := range []string{"test", "check"} {
			if makeHasTarget(root, target) {
				return "make " + target
			}
		}
		return "make"
	}
	return "make"
}

// makeHasTarget checks whether a Makefile defines the given target.
func makeHasTarget(root, target string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "make", "-n", target)
	cmd.Dir = root
	return cmd.Run() == nil
}

// goVerifyScope returns Go package arguments scoped to the edited files
// plus their reverse dependencies (packages that import the edited packages).
// If "files" is set in flags, it computes the unique ./dir packages, then
// uses `go list` to find importers so cross-package breakage is caught.
// Falls back to "./..." when no files are specified.
func goVerifyScope(root string, flags map[string]any) string {
	files, _ := flags["files"].([]string)
	if len(files) == 0 {
		// No files specified (standalone verify): use go list to get valid
		// packages but exclude known non-production dirs. Falls back to
		// "./..." if go list fails or returns nothing.
		scope := goListValidPackages(root)
		if scope == "" {
			scope = "./..."
		}
		return scope
	}
	edited := map[string]bool{}
	for _, f := range files {
		dir := filepath.Dir(f)
		if dir == "" || dir == "." {
			dir = "."
		}
		if dir != "." && !strings.HasPrefix(dir, "./") {
			dir = "./" + dir
		}
		edited[dir] = true
	}

	// Try to expand scope to include reverse dependencies (importers).
	// This catches cross-package breakage when a public symbol is renamed
	// or removed. If go list fails, fall back to just the edited packages.
	if importers := goReverseImporters(root, edited); len(importers) > 0 {
		for _, pkg := range importers {
			edited[pkg] = true
		}
	}

	parts := make([]string, 0, len(edited))
	for dir := range edited {
		parts = append(parts, dir)
	}
	return strings.Join(parts, " ")
}

// goListValidPackages uses `go list` to enumerate valid packages, excluding
// dirs with build errors (scratch/, tmp_*, testdata/, etc). Returns space-joined
// package dirs or empty string if go list fails.
// excludedVerifyDirs are directory prefixes excluded from default verify scope.
// These commonly contain fixtures, scratch work, or intentionally broken code.
var excludedVerifyDirs = []string{
	"testdata/", "scratch/", "tmp_", "examples/", "vendor/",
}

func goListValidPackages(root string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Only include packages with non-test Go files (go build fails on test-only packages)
	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-f",
		`{{if .GoFiles}}{{.Dir}}{{end}}`, "./...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	var pkgs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		rel, err := filepath.Rel(root, line)
		if err != nil {
			continue
		}
		// Skip excluded directories
		excluded := false
		for _, prefix := range excludedVerifyDirs {
			trimmed := strings.TrimSuffix(prefix, "/")
			if rel == trimmed || strings.HasPrefix(rel, prefix) || strings.HasPrefix(rel, trimmed+"/") {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		if rel == "." {
			pkgs = append(pkgs, ".")
		} else {
			pkgs = append(pkgs, "./"+rel)
		}
	}
	if len(pkgs) == 0 {
		return ""
	}
	return strings.Join(pkgs, " ")
}

// goReverseImporters finds packages that import any of the edited packages.
// It shells out to `go list` which adds ~100ms but catches breakage that
// scoped builds miss. Returns relative package dirs (./internal/dispatch etc).
func goReverseImporters(root string, editedDirs map[string]bool) []string {
	// Get module path from go.mod to convert dirs to import paths.
	modData, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil
	}
	modulePath := ""
	for _, line := range strings.Split(string(modData), "\n") {
		if strings.HasPrefix(line, "module ") {
			modulePath = strings.TrimSpace(strings.TrimPrefix(line, "module"))
			break
		}
	}
	if modulePath == "" {
		return nil
	}

	// Convert edited dirs to full import paths for matching.
	editedPkgs := map[string]bool{}
	for dir := range editedDirs {
		clean := strings.TrimPrefix(dir, "./")
		if clean == "." || clean == "" {
			editedPkgs[modulePath] = true
		} else {
			editedPkgs[modulePath+"/"+clean] = true
		}
	}

	// Ask go list for all packages and their imports.
	// Use -e to tolerate broken packages (scratch/, tmp_agent_flow/, etc).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-f",
		`{{.ImportPath}} {{join .Imports ","}}`, "./...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var importers []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) < 2 {
			continue
		}
		pkgPath := parts[0]
		imports := strings.Split(parts[1], ",")
		for _, imp := range imports {
			if editedPkgs[imp] {
				// Convert import path back to relative dir.
				rel := strings.TrimPrefix(pkgPath, modulePath)
				if rel == "" {
					rel = "."
				} else {
					rel = "./" + strings.TrimPrefix(rel, "/")
				}
				importers = append(importers, rel)
				break
			}
		}
	}
	return importers
}
