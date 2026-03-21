package dispatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jordw/edr/internal/index"
)

// DispatchVerify runs verification without requiring a DB or index.
// This avoids creating .edr as a side effect on unindexed repos.
func DispatchVerify(ctx context.Context, root string, args []string, flags map[string]any) (any, error) {
	return runVerify(ctx, nil, root, args, flags)
}

func runVerify(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	command := flagString(flags, "command", "")
	level := flagString(flags, "level", "build")
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
		} else {
			return nil, fmt.Errorf("verify: no command specified and could not auto-detect project type")
		}
	}

	timeout := flagInt(flags, "timeout", 120)
	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "sh", "-c", command)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()

	result := map[string]any{
		"command": command,
		"output":  string(out),
	}

	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			result["status"] = "timeout"
			
			result["error"] = fmt.Sprintf("timeout after %ds (may need longer for cold builds with dependency downloads)", timeout)
		} else {
			result["status"] = "failed"
			
			result["error"] = err.Error()
		}
	} else {
		result["status"] = "passed"
			}

	return result, nil
}

// goVerifyScope returns Go package arguments scoped to the edited files
// plus their reverse dependencies (packages that import the edited packages).
// If "files" is set in flags, it computes the unique ./dir packages, then
// uses `go list` to find importers so cross-package breakage is caught.
// Falls back to "./..." when no files are specified.
func goVerifyScope(root string, flags map[string]any) string {
	files, _ := flags["files"].([]string)
	if len(files) == 0 {
		// No files specified: use go list to get valid packages, excluding
		// dirs that aren't part of the main module (testdata, scratch, tmp_*, etc).
		scope := goListValidPackages(root)
		if scope != "" {
			return scope
		}
		return "./..."
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
