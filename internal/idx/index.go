// Package idx is edr's trigram + symbol index. The package is split
// across several files by concern:
//
//   - build.go   — full index construction (BuildFull, BuildFullFromWalk,
//                  BuildFullFromWalkWithImports, rebuildSmart)
//   - tick.go    — incremental reconciliation against .git/index
//   - patch.go   — dirty-file patching (PatchDirtyFiles)
//   - query.go   — trigram lookup + posting intersection (Query)
//   - stat.go    — StatChanges: parallel stat walk for IncrementalTick
//   - load.go    — in-memory index loaders (loadIndex, loadIndexTrigrams)
//   - dirty.go   — dirty-tracker shims over internal/staleness
//
// index.go (this file) holds the top-level constants, the Status type,
// and IsComplete — the small glue that doesn't fit elsewhere.
package idx

import (
	"os"
	"path/filepath"
)

// MainFile is the index filename within the edr repo directory.
const MainFile = "trigram.idx"

// DirtyFile is the on-disk name of the dirty marker.
// Kept as a public constant for callers that reference it directly.
const DirtyFile = "trigram.dirty"

// IsComplete returns true if the index exists and is not stale, meaning
// it covers all repo files and the unindexed-file walk can be skipped.
func IsComplete(repoRoot, edrDir string) bool {
	if IsDirty(edrDir) {
		return false
	}
	h, err := ReadHeader(edrDir)
	if err != nil {
		return false
	}
	return h.GitMtime != 0 && gitIndexMtime(repoRoot) == h.GitMtime
}

// Status holds index stats for edr index --status.
type Status struct {
	Exists    bool
	Files     int
	Trigrams  int
	SizeBytes int64
	Stale     bool
	GitMtime  int64
}

// GetStatus returns the current index status.
func GetStatus(repoRoot, edrDir string) Status {
	s := Status{}
	mainPath := filepath.Join(edrDir, MainFile)
	info, err := os.Stat(mainPath)
	if err != nil {
		s.Stale = true
		return s
	}
	s.Exists = true
	s.SizeBytes = info.Size()
	if h, err := ReadHeader(edrDir); err == nil {
		s.Files = int(h.NumFiles)
		s.Trigrams = int(h.NumTrigrams)
		s.GitMtime = h.GitMtime
		s.Stale = gitIndexMtime(repoRoot) != h.GitMtime || IsDirty(edrDir)
	} else {
		s.Stale = true
	}
	return s
}
