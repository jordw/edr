package session

import "strings"

// AssumptionEntry tracks a signature snapshot for a symbol the agent has read.
type AssumptionEntry struct {
	SigHash string `json:"sig_hash"` // SHA256 prefix of the signature string
	OpID    string `json:"op_id"`    // op ID when the assumption was recorded (e.g., "r3")
}

type StaleAssumption struct {
	Key, File, Symbol, AssumedAt, Current string
}

func SigHash(sig string) string { return ContentHash(sig) }

func (s *Session) RecordAssumption(key, sig, opID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Assumptions == nil {
		s.Assumptions = make(map[string]AssumptionEntry)
	}
	s.Assumptions[key] = AssumptionEntry{SigHash: SigHash(sig), OpID: opID}
}

// UpdateAssumptionOpID updates just the op ID for an existing assumption.
func (s *Session) UpdateAssumptionOpID(key, opID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.Assumptions[key]; ok {
		entry.OpID = opID
		s.Assumptions[key] = entry
	}
}

func (s *Session) CheckAssumptions(currentSigs map[string]string) []StaleAssumption {
	s.mu.Lock()
	defer s.mu.Unlock()
	var stale []StaleAssumption
	for key, entry := range s.Assumptions {
		cur, ok := currentSigs[key]
		if !ok {
			stale = append(stale, StaleAssumption{Key: key, AssumedAt: entry.OpID})
			continue
		}
		if cur != entry.SigHash {
			stale = append(stale, StaleAssumption{Key: key, AssumedAt: entry.OpID, Current: cur})
		}
	}
	for i := range stale {
		if idx := strings.IndexByte(stale[i].Key, ':'); idx > 0 {
			stale[i].File = stale[i].Key[:idx]
			stale[i].Symbol = stale[i].Key[idx+1:]
		}
	}
	return stale
}

func (s *Session) GetAssumptions() map[string]AssumptionEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]AssumptionEntry, len(s.Assumptions))
	for k, v := range s.Assumptions {
		out[k] = v
	}
	return out
}

func (s *Session) ClearAssumption(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Assumptions, key)
}
