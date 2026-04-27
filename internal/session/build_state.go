package session

func (s *Session) RecordVerify(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastVerifyStatus = status
	s.EditsSinceVerify = false
}

func (s *Session) RecordEdit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.EditsSinceVerify = true
}

func (s *Session) BuildState() (status string, editsSince bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.LastVerifyStatus == "" {
		return "", false
	}
	if s.EditsSinceVerify {
		return "unknown", true
	}
	return s.LastVerifyStatus, false
}

// CheckRunOutput checks whether command output matches the previously stored hash.
// Returns "unchanged" if the output is identical, "new" otherwise.
func (s *Session) CheckRunOutput(key string, output string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.RunHashes == nil {
		return "new"
	}
	prev, ok := s.RunHashes[key]
	if !ok {
		return "new"
	}
	if ContentHash(output) == prev {
		return "unchanged"
	}
	return "new"
}

// StoreRunOutput stores the output hash for a command run.
func (s *Session) StoreRunOutput(key string, output string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.RunHashes == nil {
		s.RunHashes = make(map[string]string)
	}
	s.RunHashes[key] = ContentHash(output)
}

// ClearRunOutput removes the stored hash for a command key.
func (s *Session) ClearRunOutput(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.RunHashes, key)
}
