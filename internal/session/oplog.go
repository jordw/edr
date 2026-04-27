package session

import "fmt"

// OpEntry records a single operation in the session op log.
// Used by `edr next` to show recent activity for re-orientation.
type OpEntry struct {
	OpID   string `json:"op_id"`   // "e7", "r3", "s1"
	Cmd    string `json:"cmd"`     // "edit", "read", "search"
	File   string `json:"file"`    // relative path
	Symbol string `json:"symbol"`  // symbol name, if any
	Action string `json:"action"`  // raw operation: "replace_text", "delete", "insert_at", "read_symbol"
	Kind   string `json:"kind"`    // display label: "signature_changed", "text_replaced", "symbol_read"
	OK     bool   `json:"ok"`      // success/failure
}

// MaxOpLogEntries is the sliding window size for the op log.
const MaxOpLogEntries = 100

func (s *Session) RecordOp(cmd, file, symbol, action, kind string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := "x"
	if len(cmd) > 0 {
		prefix = string(cmd[0])
	}
	s.opCount++
	opID := fmt.Sprintf("%s%d", prefix, s.opCount)
	s.OpLog = append(s.OpLog, OpEntry{OpID: opID, Cmd: cmd, File: file, Symbol: symbol, Action: action, Kind: kind, OK: ok})
	if len(s.OpLog) > MaxOpLogEntries {
		s.OpLog = s.OpLog[len(s.OpLog)-MaxOpLogEntries:]
	}
}

func (s *Session) GetRecentOps(n int) []OpEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 || n > len(s.OpLog) {
		out := make([]OpEntry, len(s.OpLog))
		copy(out, s.OpLog)
		return out
	}
	start := len(s.OpLog) - n
	out := make([]OpEntry, n)
	copy(out, s.OpLog[start:])
	return out
}

// EditsSinceRead returns the number of successful edits to file since the most
// recent successful read of that file. If the file has not been read in the
// op log window, returns the count of all successful edits to it.
func (s *Session) EditsSinceRead(file string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	lastRead := -1
	for i := len(s.OpLog) - 1; i >= 0; i-- {
		op := s.OpLog[i]
		if op.File != file || !op.OK {
			continue
		}
		if op.Cmd == "read" || op.Cmd == "focus" {
			lastRead = i
			break
		}
	}
	count := 0
	for i := lastRead + 1; i < len(s.OpLog); i++ {
		op := s.OpLog[i]
		if op.OK && op.File == file && op.Cmd == "edit" {
			count++
		}
	}
	return count
}

func (s *Session) GetFocus() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Focus
}

func (s *Session) SetFocus(focus string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Focus = focus
}
