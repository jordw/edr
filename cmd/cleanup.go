package cmd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const cleanupInterval = 1 * time.Hour

// cleanEdrDir removes stale files from the .edr directory:
// - Session JSON files older than 7 days
// - PPID mapping files for dead processes
// - Run baseline (.last) files older than 7 days
func cleanEdrDir(edrDir string) {
	cutoff := time.Now().Add(-7 * 24 * time.Hour)

	// 1. Sessions: stale .json files and dead ppid_* mappings
	sessDir := filepath.Join(edrDir, "sessions")
	entries, _ := os.ReadDir(sessDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		path := filepath.Join(sessDir, name)

		// PPID mapping files: remove if the process is dead
		if strings.HasPrefix(name, "ppid_") {
			pidStr := strings.TrimPrefix(name, "ppid_")
			pid, err := strconv.Atoi(pidStr)
			if err != nil || !processAlive(pid) {
				os.Remove(path)
			}
			continue
		}

		// Session JSON files: remove if older than 7 days
		if strings.HasSuffix(name, ".json") {
			info, err := e.Info()
			if err != nil || info.ModTime().Before(cutoff) {
				os.Remove(path)
			}
		}
	}

	// 2. Run baselines: stale .last files
	runDir := filepath.Join(edrDir, "delta")
	entries, _ = os.ReadDir(runDir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".last") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(runDir, e.Name()))
		}
	}
}

// maybeCleanEdrDir runs cleanEdrDir at most once per cleanupInterval.
// Uses a timestamp file (.edr/last_cleanup) to rate-limit.
func maybeCleanEdrDir(edrDir string) {
	marker := filepath.Join(edrDir, "last_cleanup")
	info, err := os.Stat(marker)
	if err == nil && time.Since(info.ModTime()) < cleanupInterval {
		return
	}
	// Touch the marker first (even if cleanup fails) to avoid retry storms
	os.WriteFile(marker, nil, 0600)
	cleanEdrDir(edrDir)
}

// processAlive checks if a process with the given PID is still running.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// signal 0 tests existence without actually sending a signal.
	// ESRCH means the process doesn't exist.
	// EPERM means it exists but we can't signal it (different user) — still alive.
	// nil means we can signal it — alive.
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
