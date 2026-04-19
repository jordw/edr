package idx

import (
	"strings"

	"github.com/jordw/edr/internal/staleness"
)

// dirtyName is the staleness.Tracker name for the trigram index. The
// on-disk file is edrDir/<dirtyName>.dirty — matching the historical
// DirtyFile constant so existing .edr/ dirs remain readable.
const dirtyName = "trigram"

// dirtyTracker returns the staleness.Tracker backing the trigram
// dirty list. A fresh Tracker is returned per call — they hold no
// connection state and only serialize in-process Mark/Clear via a
// mutex on their own path.
func dirtyTracker(edrDir string) *staleness.Tracker {
	return staleness.OpenTracker(edrDir, dirtyName)
}

// MarkDirty signals that specific files were edited and the index may be stale.
// Appends the given relative paths to the dirty set.
func MarkDirty(edrDir string, files ...string) {
	// Filter out paths that look like legacy junk to preserve the
	// prior "must look like a path" contract.
	var clean []string
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f == "" || f == "1" {
			continue
		}
		if !strings.Contains(f, "/") && !strings.Contains(f, ".") {
			continue
		}
		clean = append(clean, f)
	}
	dirtyTracker(edrDir).Mark(clean...)
}

// ClearDirty removes the dirty marker after a full index build.
func ClearDirty(edrDir string) {
	dirtyTracker(edrDir).Clear()
}

// IsDirty returns true if any files have been edited since the last index build.
func IsDirty(edrDir string) bool {
	return dirtyTracker(edrDir).IsDirty()
}

// IsDirtyFile returns true if a specific file has been edited since the last build.
func IsDirtyFile(edrDir, relPath string) bool {
	return dirtyTracker(edrDir).Has(relPath)
}

// DirtyFiles returns the set of files edited since the last index build.
func DirtyFiles(edrDir string) []string {
	out := dirtyTracker(edrDir).Dirty()
	// Preserve the legacy "must look like a path" filter so any
	// pre-existing dirty files with weird lines don't pollute the
	// rebuild.
	kept := out[:0]
	for _, f := range out {
		if strings.Contains(f, "/") || strings.Contains(f, ".") {
			kept = append(kept, f)
		}
	}
	return kept
}
