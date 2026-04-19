package idx

import (
	"github.com/jordw/edr/internal/status"
)

// Reporter produces a status.Report for the trigram+symbol index at edrDir.
// Construct with NewReporter and call Status — which is also the method
// that satisfies status.Reporter.
type Reporter struct {
	repoRoot string
	edrDir   string
}

// NewReporter returns a status.Reporter for the index at edrDir.
// repoRoot is used only to compare the index git-mtime against the
// current working tree; callers outside a repo may pass "".
func NewReporter(repoRoot, edrDir string) *Reporter {
	return &Reporter{repoRoot: repoRoot, edrDir: edrDir}
}

// Status returns a status.Report for the index. Populated fields:
//
//   - Name:     "index"
//   - Exists:   main index file on disk
//   - Files:    #files indexed (from header)
//   - Bytes:    on-disk size of the main index
//   - Stale:    git-mtime mismatch OR a dirty-file marker is set
//   - Coverage: 0 — the index cannot cheaply count working-tree files;
//               callers that need coverage compute it themselves
//   - Extra:    {"trigrams": int, "symbols": int, "git_mtime": int64}
//               ("symbols" is omitted when header reports 0)
func (r *Reporter) Status() status.Report {
	legacy := GetStatus(r.repoRoot, r.edrDir)
	rep := status.Report{
		Name:   "index",
		Exists: legacy.Exists,
		Files:  legacy.Files,
		Bytes:  legacy.SizeBytes,
		Stale:  legacy.Stale,
	}
	if legacy.Exists {
		extra := map[string]any{
			"trigrams":  legacy.Trigrams,
			"git_mtime": legacy.GitMtime,
		}
		if h, err := ReadHeader(r.edrDir); err == nil && h.NumSymbols > 0 {
			extra["symbols"] = int(h.NumSymbols)
		}
		rep.Extra = extra
	}
	return rep
}
