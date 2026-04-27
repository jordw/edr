package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ResolveSessionID returns the session ID from the EDR_SESSION env var.
// Returns "" when unset, which means no session (ephemeral, no dedup).
// Agents must explicitly set EDR_SESSION to opt into session features.
func ResolveSessionID() string {
	id := os.Getenv("EDR_SESSION")
	if id != "" {
		return id
	}
	return resolveByPPID()
}

// stableAncestorPID finds the stable process that launched edr. It walks up
// the process tree from edr's parent looking for a shell (zsh, bash, sh,
// fish). If found, the shell's parent is the agent/terminal — which is
// stable across tool calls. If no shell is found (agent invokes edr directly),
// the direct parent is already the stable process.
func stableAncestorPID() int {
	pid := os.Getppid()
	name := processName(pid)
	if isShell(name) {
		if parent := parentPID(pid); parent > 1 {
			return parent
		}
	}
	return pid
}

var shells = map[string]bool{
	"zsh": true, "bash": true, "sh": true, "fish": true,
	"dash": true, "ksh": true, "csh": true, "tcsh": true,
	"-zsh": true, "-bash": true, "-sh": true, "-fish": true,
}

func isShell(name string) bool {
	return shells[name] || shells[filepath.Base(name)]
}

// processName returns the command name for a PID, or "" on error.
func processName(pid int) string {
	// Linux: /proc/<pid>/comm
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
		return strings.TrimSpace(string(data))
	}
	// macOS: ps -o comm= -p <pid>
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return filepath.Base(strings.TrimSpace(string(out)))
}

// parentPID returns the parent PID of a process, or 0 on error.
func parentPID(pid int) int {
	// Linux: /proc/<pid>/stat
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		s := string(data)
		if i := strings.LastIndex(s, ") "); i >= 0 {
			fields := strings.Fields(s[i+2:])
			if len(fields) >= 2 {
				if n, err := strconv.Atoi(fields[1]); err == nil {
					return n
				}
			}
		}
		return 0
	}
	// macOS: ps -o ppid= -p <pid>
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil {
		return n
	}
	return 0
}

func resolveByPPID() string {
	root, err := findRepoRoot()
	if err != nil {
		// No .git — fall back to cwd. The rest of edr tolerates non-git dirs;
		// session resolution should too, otherwise state silently vanishes
		// between subprocess calls.
		cwd, werr := os.Getwd()
		if werr != nil {
			return ""
		}
		if resolved, rerr := filepath.EvalSymlinks(cwd); rerr == nil {
			cwd = resolved
		}
		root = cwd
	}
	edrDir := homeEdrDir(root)
	sessDir := filepath.Join(edrDir, "sessions")
	pid := stableAncestorPID()
	path := filepath.Join(sessDir, fmt.Sprintf("ppid_%d", pid))
	startTime := processStartTime(pid)

	// Read existing mapping (format: "session_id\nstart_time").
	// Validate that the process start time still matches to detect PID reuse.
	if data, err := os.ReadFile(path); err == nil {
		lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
		if len(lines) >= 1 && lines[0] != "" {
			id := lines[0]
			if len(lines) == 2 {
				// Validate start time — if it changed, PID was reused.
				if startTime != "" && startTime == lines[1] {
					return id
				}
				// Start time mismatch or unavailable — fall through to create new.
			} else {
				// Legacy format (no start time) — accept but upgrade on next write.
				return id
			}
		}
	}

	// No valid mapping — create a fresh session for this process.
	id := GenerateID()
	os.MkdirAll(sessDir, 0700)
	writePPIDMapping(path, id, startTime)
	return id
}

// processStartTime returns a stable string identifying when a process started.
// Used to detect PID reuse: if a PID's start time doesn't match what we
// recorded, the PID was recycled by the OS for a different process.
func processStartTime(pid int) string {
	// Linux: /proc/<pid>/stat field 22 (starttime in clock ticks)
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		s := string(data)
		if i := strings.LastIndex(s, ") "); i >= 0 {
			fields := strings.Fields(s[i+2:])
			if len(fields) >= 20 {
				return fields[19] // starttime
			}
		}
		return ""
	}
	// macOS: ps -o lstart= -p <pid> (e.g., "Sat Mar 22 19:30:00 2026")
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// writePPIDMapping writes a ppid mapping file with format "session_id\nstart_time".
func writePPIDMapping(path, id, startTime string) {
	content := id
	if startTime != "" {
		content = id + "\n" + startTime
	}
	os.WriteFile(path, []byte(content), 0600)
}

// WriteSessionMapping writes a PPID mapping file for the stable ancestor.
func WriteSessionMapping(sessDir, id string) {
	pid := stableAncestorPID()
	path := filepath.Join(sessDir, fmt.Sprintf("ppid_%d", pid))
	writePPIDMapping(path, id, processStartTime(pid))
}

// findRepoRoot walks up from cwd to find .git directory.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	// Resolve symlinks so /tmp → /private/tmp (macOS) matches NormalizeRoot.
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no git repo found")
		}
		dir = parent
	}
}

// homeEdrDir computes the per-repo edr data directory under ~/.edr/repos/.
// Mirrors index.HomeEdrDir but avoids the cross-package dependency.
func homeEdrDir(repoRoot string) string {
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		abs = repoRoot
	}
	// Resolve symlinks so /tmp → /private/tmp (macOS) produces a stable key.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	var base string
	if override := os.Getenv("EDR_HOME"); override != "" {
		base = override
	} else if home, err := os.UserHomeDir(); err == nil {
		base = filepath.Join(home, ".edr")
	} else {
		dir := filepath.Join(abs, ".edr")
		os.MkdirAll(dir, 0700)
		return dir
	}
	h := sha256.Sum256([]byte(abs))
	key := hex.EncodeToString(h[:])[:12] + "_" + filepath.Base(abs)
	dir := filepath.Join(base, "repos", key)
	os.MkdirAll(dir, 0700)
	return dir
}

// GenerateID creates a short unique session ID (8 hex chars from timestamp + random).
func GenerateID() string {
	b := make([]byte, 4)
	// Use crypto/rand for uniqueness
	rand.Read(b)
	return hex.EncodeToString(b)
}

// LoadSession loads the session identified by EDR_SESSION env var.
// Returns the session and a save function. Call save() after processing
// to persist changes. If EDR_SESSION is not set, returns an ephemeral
// session and a no-op save.
func LoadSession(edrDir string, repoRoots ...string) (*Session, func()) {
	id := ResolveSessionID()
	if id == "" {
		return New(), func() {}
	}
	path := filepath.Join(edrDir, "sessions", id+".json")
	sess := LoadFromFile(path)
	if len(repoRoots) > 0 && repoRoots[0] != "" {
		sess.repoRoot = repoRoots[0]
	}
	return sess, func() {
		sess.SaveToFile(path)
	}
}
