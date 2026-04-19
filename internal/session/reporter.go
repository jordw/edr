package session

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/status"
)

// Reporter produces a status.Report for the session store at
// edrDir/sessions/.
type Reporter struct {
	edrDir string
}

// NewReporter returns a status.Reporter for the session store under edrDir.
func NewReporter(edrDir string) *Reporter {
	return &Reporter{edrDir: edrDir}
}

// Status returns a status.Report for the session store. Populated fields:
//
//   - Name:   "session"
//   - Exists: at least one session file exists under edrDir/sessions/
//   - Files:  number of session files (cp_*.json checkpoints excluded)
//   - Bytes:  total size of session + checkpoint files
//   - Stale:  always false — sessions are not derived from source of truth
//   - Extra:  {"checkpoints": int} — count of auto + manual checkpoints
//
// Session and checkpoint files are small JSON blobs; one Readdir plus
// cheap Stat per entry is fine on every command.
func (r *Reporter) Status() status.Report {
	rep := status.Report{Name: "session"}
	sessDir := filepath.Join(r.edrDir, "sessions")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		return rep
	}

	sessions := 0
	checkpoints := 0
	var bytes int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		bytes += info.Size()
		if strings.HasPrefix(name, "cp_") {
			checkpoints++
		} else {
			sessions++
		}
	}

	rep.Files = sessions
	rep.Bytes = bytes
	rep.Exists = sessions > 0 || checkpoints > 0
	if checkpoints > 0 {
		rep.Extra = map[string]any{"checkpoints": checkpoints}
	}
	return rep
}
