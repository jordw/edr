package dispatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jordw/edr/internal/index"
)

func runVerify(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	command := flagString(flags, "command", "")
	if command == "" {
		// Auto-detect based on project files
		if _, err := os.Stat(root + "/go.mod"); err == nil {
			command = "go build ./..."
		} else if _, err := os.Stat(root + "/package.json"); err == nil {
			command = "npx tsc --noEmit"
		} else if _, err := os.Stat(root + "/Cargo.toml"); err == nil {
			command = "cargo check"
		} else {
			return nil, fmt.Errorf("verify: no command specified and could not auto-detect project type")
		}
	}

	timeout := flagInt(flags, "timeout", 30)
	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "sh", "-c", command)
	cmd.Dir = root
	// Inherit environment and set GOCACHE for sandboxed environments
	cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(root, ".edr", "gocache"))
	out, err := cmd.CombinedOutput()

	result := map[string]any{
		"command": command,
		"output":  string(out),
	}

	if err != nil {
		result["ok"] = false
		result["error"] = err.Error()
	} else {
		result["ok"] = true
	}

	return result, nil
}
