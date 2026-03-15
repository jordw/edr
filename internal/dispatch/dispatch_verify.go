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
		result["ok"] = false
		if cmdCtx.Err() == context.DeadlineExceeded {
			result["error"] = fmt.Sprintf("timeout after %ds (may need longer for cold builds with dependency downloads)", timeout)
			result["timeout"] = true
		} else {
			result["error"] = err.Error()
		}
	} else {
		result["ok"] = true
	}

	return result, nil
}

// goVerifyScope returns Go package arguments scoped to the edited files.
// If "files" is set in flags, it computes the unique ./dir/... packages.
// Falls back to "./..." when no files are specified.
func goVerifyScope(root string, flags map[string]any) string {
	files, _ := flags["files"].([]string)
	if len(files) == 0 {
		return "./..."
	}
	seen := map[string]bool{}
	for _, f := range files {
		dir := filepath.Dir(f)
		if dir == "" || dir == "." {
			dir = "."
		}
		// Normalize to ./dir form
		if dir != "." && !strings.HasPrefix(dir, "./") {
			dir = "./" + dir
		}
		seen[dir] = true
	}
	// If root package is included, just use ./...
	if seen["."] {
		return "./..."
	}
	parts := make([]string, 0, len(seen))
	for dir := range seen {
		parts = append(parts, dir)
	}
	return strings.Join(parts, " ")
}
