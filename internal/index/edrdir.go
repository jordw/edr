package index

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// HomeEdrDir returns the per-repo edr data directory under ~/.edr/repos/.
// The directory is keyed by a hash of the absolute repo root path plus the
// basename for human readability: ~/.edr/repos/<hash12>_<basename>/
// Creates the directory if it doesn't exist.
// Respects EDR_HOME env var as an override for the base directory.
// Falls back to <root>/.edr/ if the home directory is unavailable.
func HomeEdrDir(repoRoot string) string {
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		abs = repoRoot
	}

	var base string
	if override := os.Getenv("EDR_HOME"); override != "" {
		base = override
	} else if home, err := os.UserHomeDir(); err == nil {
		base = filepath.Join(home, ".edr")
	} else {
		// Fallback: in-repo .edr/ (containers, no home dir)
		dir := filepath.Join(abs, ".edr")
		os.MkdirAll(dir, 0700)
		return dir
	}

	h := sha256.Sum256([]byte(abs))
	key := hex.EncodeToString(h[:])[:12] + "_" + filepath.Base(abs)
	dir := filepath.Join(base, "repos", key)
	os.MkdirAll(dir, 0700)

	// Write breadcrumb for human identification of repo dirs
	breadcrumb := filepath.Join(dir, "root.txt")
	if _, err := os.Stat(breadcrumb); err != nil {
		os.WriteFile(breadcrumb, []byte(abs+"\n"), 0600)
	}
	return dir
}
