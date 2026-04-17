package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Checkpoint captures the full session state plus dirty file contents at a point in time.
type Checkpoint struct {
	ID        string         `json:"id"`         // "cp_1", "cp_auto_start", etc.
	Label     string         `json:"label"`       // optional human label
	CreatedAt time.Time      `json:"created_at"`
	OpID      string         `json:"op_id"`       // last op ID at checkpoint time
	Files     []FileSnapshot `json:"files"`       // dirty file snapshots
	Session   SessionSnap    `json:"session"`     // session state snapshot
}

// FileSnapshot stores the full content of a file at checkpoint time.
type FileSnapshot struct {
	Path    string `json:"path"`    // relative to repo root
	Content []byte `json:"content"` // full file content
}

// SessionSnap captures the restorable session state.
type SessionSnap struct {
	OpLog            []OpEntry                  `json:"op_log"`
	OpCount          int                        `json:"op_count"`
	Focus            string                     `json:"focus,omitempty"`
	Assumptions      map[string]AssumptionEntry `json:"assumptions,omitempty"`
	LastVerifyStatus string                     `json:"last_verify_status,omitempty"`
	EditsSinceVerify bool                       `json:"edits_since_verify,omitempty"`
	FileHashes       map[string]string          `json:"file_hashes,omitempty"`
	FileMtimes       map[string]FileMtimeEntry  `json:"file_mtimes,omitempty"`
}

// MaxUndoStack is the maximum number of auto-checkpoints kept.
// Oldest are evicted when the cap is reached.
const MaxUndoStack = 20

// CheckpointInfo is a lightweight summary for listing.
type CheckpointInfo struct {
	ID        string    `json:"id"`
	Label     string    `json:"label,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	OpID      string    `json:"op_id"`
	FileCount int       `json:"file_count"`
}

// --- Checkpoint creation ---

// CreateCheckpoint snapshots the current session state and the specified dirty files.
// repoRoot is the absolute path to the repository root.
// dirtyFiles are relative paths of files modified in this session.
// Returns the checkpoint ID.
func (s *Session) CreateCheckpoint(sessDir, repoRoot, label string, dirtyFiles []string) (*Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id, err := s.nextCheckpointID(sessDir, false)
	if err != nil {
		return nil, err
	}

	cp := s.buildCheckpoint(id, label, repoRoot, dirtyFiles)
	if err := saveCheckpoint(sessDir, cp); err != nil {
		return nil, fmt.Errorf("save checkpoint: %w", err)
	}
	return cp, nil
}

// CreateAutoCheckpoint creates a rolling auto-checkpoint, evicting the oldest if at cap.
func (s *Session) CreateAutoCheckpoint(sessDir, repoRoot, label string, dirtyFiles []string) (*Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Sequential IDs: find the highest existing auto-checkpoint number
	existing := ListCheckpoints(sessDir)
	maxNum := 0
	for _, cp := range existing {
		if strings.HasPrefix(cp.ID, "cp_auto_") {
			numStr := cp.ID[len("cp_auto_"):]
			var n int
			if _, err := fmt.Sscanf(numStr, "%d", &n); err == nil && n > maxNum {
				maxNum = n
			}
		}
	}
	id := fmt.Sprintf("cp_auto_%d", maxNum+1)

	cp := s.buildCheckpoint(id, label, repoRoot, dirtyFiles)
	if err := saveCheckpoint(sessDir, cp); err != nil {
		return nil, fmt.Errorf("save auto checkpoint: %w", err)
	}

	s.enforceAutoCheckpointCap(sessDir)
	return cp, nil
}

func (s *Session) buildCheckpoint(id, label, repoRoot string, dirtyFiles []string) *Checkpoint {
	// Determine last op ID
	lastOpID := ""
	if len(s.OpLog) > 0 {
		lastOpID = s.OpLog[len(s.OpLog)-1].OpID
	}

	// Snapshot files. For files that do not exist yet (will be created by
	// the upcoming edit), record them with nil content so restore knows to
	// delete them.
	var files []FileSnapshot
	for _, rel := range dirtyFiles {
		abs := filepath.Join(repoRoot, rel)
		content, err := os.ReadFile(abs)
		if err != nil {
			// File does not exist — record with nil content (new file marker)
			files = append(files, FileSnapshot{Path: rel, Content: nil})
			continue
		}
		files = append(files, FileSnapshot{Path: rel, Content: content})
	}

	// Snapshot session state
	snap := SessionSnap{
		OpLog:            make([]OpEntry, len(s.OpLog)),
		OpCount:          s.opCount,
		Focus:            s.Focus,
		LastVerifyStatus: s.LastVerifyStatus,
		EditsSinceVerify: s.EditsSinceVerify,
	}
	copy(snap.OpLog, s.OpLog)

	snap.Assumptions = maps.Clone(s.Assumptions)
	snap.FileHashes = maps.Clone(s.FileHashes)
	snap.FileMtimes = maps.Clone(s.FileMtimes)
	return &Checkpoint{
		ID:        id,
		Label:     label,
		CreatedAt: time.Now(),
		OpID:      lastOpID,
		Files:     files,
		Session:   snap,
	}
}

// --- Restore ---

// RestoreCheckpoint reverts files and session state to the given checkpoint.
// If saveCurrentFirst is true (default), it creates a pre-restore checkpoint first.
// Returns the list of files that were restored and any new files created after
// the checkpoint that were NOT removed.
func (s *Session) RestoreCheckpoint(sessDir, repoRoot, cpID string, saveCurrentFirst bool, currentDirtyFiles []string) (restored []string, notRemoved []string, preRestoreID string, err error) {
	// Load the target checkpoint
	cp, err := loadCheckpoint(sessDir, cpID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("load checkpoint %q: %w", cpID, err)
	}

	// Save current state as pre-restore checkpoint
	if saveCurrentFirst {
		s.mu.Lock()
		preID := fmt.Sprintf("cp_pre_restore_%s", cpID)
		preCp := s.buildCheckpoint(preID, fmt.Sprintf("pre-restore (before reverting to %s)", cpID), repoRoot, currentDirtyFiles)
		if saveErr := saveCheckpoint(sessDir, preCp); saveErr == nil {
			preRestoreID = preID
		}
		s.mu.Unlock()
	}

	// Restore files. Files with nil content didn't exist at checkpoint time
	// — delete them to undo the creation. Only report files that actually changed.
	cpFileSet := make(map[string]bool, len(cp.Files))
	for _, f := range cp.Files {
		cpFileSet[f.Path] = true
		abs := filepath.Join(repoRoot, f.Path)
		if f.Content == nil {
			// File didn't exist at checkpoint time — delete it
			if err := os.Remove(abs); err == nil {
				restored = append(restored, f.Path)
			}
			continue
		}
		// Skip files whose content already matches the checkpoint.
		if cur, err := os.ReadFile(abs); err == nil && bytes.Equal(cur, f.Content) {
			continue
		}
		if err := atomicWrite(abs, f.Content); err != nil {
			return restored, nil, preRestoreID, fmt.Errorf("restore %s: %w", f.Path, err)
		}
		restored = append(restored, f.Path)
	}

	// Detect files created after checkpoint that we can't restore
	for _, f := range currentDirtyFiles {
		if !cpFileSet[f] {
			notRemoved = append(notRemoved, f)
		}
	}

	// Restore session state
	s.mu.Lock()
	s.OpLog = make([]OpEntry, len(cp.Session.OpLog))
	copy(s.OpLog, cp.Session.OpLog)
	s.opCount = cp.Session.OpCount
	s.Focus = cp.Session.Focus
	s.LastVerifyStatus = cp.Session.LastVerifyStatus
	s.EditsSinceVerify = cp.Session.EditsSinceVerify

	s.Assumptions = maps.Clone(cp.Session.Assumptions)
	if cp.Session.FileHashes != nil {
		s.FileHashes = maps.Clone(cp.Session.FileHashes)
	} else {
		s.FileHashes = make(map[string]string)
	}
	if cp.Session.FileMtimes != nil {
		s.FileMtimes = maps.Clone(cp.Session.FileMtimes)
	} else {
		s.FileMtimes = make(map[string]FileMtimeEntry)
	}

	// Invalidate all content caches (conservative — agent needs fresh reads)
	s.FileContent = make(map[string]ContentEntry)
	s.SymbolContent = make(map[string]ContentEntry)
	s.SeenBodies = make(map[string]string)
	s.Diffs = make(map[string]string)

	// Record the restore as an op
	s.opCount++
	restoreOpID := fmt.Sprintf("c%d", s.opCount)
	s.OpLog = append(s.OpLog, OpEntry{
		OpID:   restoreOpID,
		Cmd:    "checkpoint",
		File:   cpID,
		Symbol: "",
		Action: "restore",
		Kind:   "checkpoint_restored",
		OK:     true,
	})
	s.mu.Unlock()

	return restored, notRemoved, preRestoreID, nil
}

// --- List / Drop / Diff ---

// ListCheckpoints returns summaries of all checkpoints for this session.
func ListCheckpoints(sessDir string) []CheckpointInfo {
	pattern := filepath.Join(sessDir, "cp_*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return nil
	}

	var infos []CheckpointInfo
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cp Checkpoint
		if json.Unmarshal(data, &cp) != nil {
			continue
		}
		infos = append(infos, CheckpointInfo{
			ID:        cp.ID,
			Label:     cp.Label,
			CreatedAt: cp.CreatedAt,
			OpID:      cp.OpID,
			FileCount: len(cp.Files),
		})
	}

	sort.Slice(infos, func(i, j int) bool {
		return infos[i].CreatedAt.Before(infos[j].CreatedAt)
	})
	return infos
}

// LatestAutoCheckpoint returns the most recent auto-checkpoint ID, or "" if none.
func LatestAutoCheckpoint(sessDir string) string {
	infos := ListCheckpoints(sessDir)
	// Walk backwards (sorted by time ascending) to find most recent auto
	for i := len(infos) - 1; i >= 0; i-- {
		if strings.HasPrefix(infos[i].ID, "cp_auto_") {
			return infos[i].ID
		}
	}
	return ""
}

// LoadCheckpoint exposes a checkpoint by ID for diff rendering.
func LoadCheckpoint(sessDir, cpID string) (*Checkpoint, error) {
	return loadCheckpoint(sessDir, cpID)
}

// DropCheckpoint removes a checkpoint file.
func DropCheckpoint(sessDir, cpID string) error {
	path := filepath.Join(sessDir, cpID+".json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("checkpoint %q not found", cpID)
	}
	return os.Remove(path)
}

// PatchCheckpointFiles adds file snapshots to an existing checkpoint. Files
// already present are not overwritten. This is used after multi-file mutations
// (rename, changesig) to add pre-mutation content for secondary files that
// weren't known at checkpoint creation time.
func PatchCheckpointFiles(sessDir, cpID, repoRoot string, oldContents map[string][]byte) error {
	cp, err := loadCheckpoint(sessDir, cpID)
	if err != nil {
		return err
	}

	// Build set of files already in checkpoint
	existing := make(map[string]bool, len(cp.Files))
	for _, f := range cp.Files {
		existing[f.Path] = true
	}

	// Add missing files
	changed := false
	for rel, content := range oldContents {
		if existing[rel] {
			continue
		}
		cp.Files = append(cp.Files, FileSnapshot{
			Path:    rel,
			Content: content,
		})
		changed = true
	}

	if !changed {
		return nil
	}

	return saveCheckpoint(sessDir, cp)
}

// DiffCheckpoint returns the files that differ between the checkpoint and current disk state.
type FileDiff struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // "modified", "deleted", "created"
}

func DiffCheckpoint(sessDir, repoRoot, cpID string, currentDirtyFiles []string) ([]FileDiff, error) {
	cp, err := loadCheckpoint(sessDir, cpID)
	if err != nil {
		return nil, err
	}

	var diffs []FileDiff
	cpFileSet := make(map[string]bool, len(cp.Files))

	for _, f := range cp.Files {
		cpFileSet[f.Path] = true
		abs := filepath.Join(repoRoot, f.Path)
		current, err := os.ReadFile(abs)
		if err != nil {
			diffs = append(diffs, FileDiff{Path: f.Path, Status: "deleted"})
			continue
		}
		if ContentHash(string(current)) != ContentHash(string(f.Content)) {
			diffs = append(diffs, FileDiff{Path: f.Path, Status: "modified"})
		}
	}

	// Files that exist now but weren't in the checkpoint
	for _, f := range currentDirtyFiles {
		if !cpFileSet[f] {
			diffs = append(diffs, FileDiff{Path: f, Status: "created"})
		}
	}

	return diffs, nil
}

// --- Helpers ---

// GetDirtyFiles derives the list of files modified in this session from the op log.
func (s *Session) GetDirtyFiles() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	seen := make(map[string]bool)
	for _, op := range s.OpLog {
		if op.File == "" {
			continue
		}
		switch op.Cmd {
		case "edit", "write", "rename":
			if op.OK {
				seen[op.File] = true
			}
		}
	}

	files := make([]string, 0, len(seen))
	for f := range seen {
		files = append(files, f)
	}
	sort.Strings(files)
	return files
}

func (s *Session) nextCheckpointID(sessDir string, isAuto bool) (string, error) {
	if isAuto {
		return "cp_auto", nil
	}

	// Count existing explicit checkpoints
	existing := ListCheckpoints(sessDir)
	explicitCount := 0
	maxNum := 0
	for _, cp := range existing {
		if strings.HasPrefix(cp.ID, "cp_auto") || strings.HasPrefix(cp.ID, "cp_pre_restore") {
			continue
		}
		explicitCount++
		// Extract number from "cp_N"
		if strings.HasPrefix(cp.ID, "cp_") {
			numStr := cp.ID[3:]
			var n int
			if _, err := fmt.Sscanf(numStr, "%d", &n); err == nil && n > maxNum {
				maxNum = n
			}
		}
	}

	// No cap on explicit checkpoints (used only by tests now)

	return fmt.Sprintf("cp_%d", maxNum+1), nil
}

func saveCheckpoint(sessDir string, cp *Checkpoint) error {
	if err := os.MkdirAll(sessDir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	path := filepath.Join(sessDir, cp.ID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadCheckpoint(sessDir, cpID string) (*Checkpoint, error) {
	path := filepath.Join(sessDir, cpID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("checkpoint %q not found", cpID)
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("corrupt checkpoint %q: %w", cpID, err)
	}
	return &cp, nil
}

func (s *Session) enforceAutoCheckpointCap(sessDir string) {
	existing := ListCheckpoints(sessDir)
	var autos []CheckpointInfo
	for _, cp := range existing {
		if strings.HasPrefix(cp.ID, "cp_auto_") {
			autos = append(autos, cp)
		}
	}
	for len(autos) > MaxUndoStack {
		DropCheckpoint(sessDir, autos[0].ID)
		autos = autos[1:]
	}
}

func atomicWrite(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp := path + ".cp_tmp"
	if err := os.WriteFile(tmp, content, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
