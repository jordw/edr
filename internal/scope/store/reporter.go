package store

import (
	"os"
	"path/filepath"

	"github.com/jordw/edr/internal/status"
)

// Reporter produces a status.Report for the scope store at edrDir.
// Construct with NewReporter.
type Reporter struct {
	edrDir string
}

// NewReporter returns a status.Reporter for the scope store at edrDir.
func NewReporter(edrDir string) *Reporter {
	return &Reporter{edrDir: edrDir}
}

// Status returns a status.Report for the scope store. Populated fields:
//
//   - Name:   "scope"
//   - Exists: scope.bin is present
//   - Files:  number of records in the header (one per indexed file)
//   - Bytes:  on-disk size of scope.bin
//   - Stale:  left false — scope staleness is per-file (checked at read
//             time by ResultFor); there is no single "the store is
//             stale" flag. Callers that want a repo-wide mismatch check
//             should count working-tree files themselves.
//
// Status does a Stat + a header-only read (no per-record decodes).
// Safe to call on every command.
func (r *Reporter) Status() status.Report {
	rep := status.Report{Name: "scope"}
	path := filepath.Join(r.edrDir, storeFileName)
	info, err := os.Stat(path)
	if err != nil {
		return rep
	}
	rep.Exists = true
	rep.Bytes = info.Size()

	idx, err := Load(r.edrDir)
	if err != nil || idx == nil {
		// File is there but unreadable (wrong version, corrupt
		// header, etc.). Report Exists+Bytes so callers see the
		// footprint; leave Files zero.
		return rep
	}
	defer idx.Close()
	if idx.header != nil {
		rep.Files = len(idx.header.Records)
	}
	return rep
}
