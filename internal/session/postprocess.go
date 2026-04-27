package session

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jordw/edr/internal/cmdspec"
)

// PostProcessStats tracks session optimization hits for trace collection.
type PostProcessStats struct {
	DeltaReads int
	BodyDedup  int
}

// --- Level 3: Body tracking ---

func (s *Session) TrackBodies(result map[string]any, cmd string) {
	// Track top-level content — check both "content" (read results) and "body" (gather)
	if body := firstString(result, "content", "body"); body != "" {
		file, _ := result["file"].(string)
		name := ""
		if s, ok := result["symbol"].(string); ok {
			name = s
		} else if sym, ok := result["symbol"].(map[string]any); ok {
			file, _ = sym["file"].(string)
			name, _ = sym["name"].(string)
		}
		if name != "" {
			s.SeenBodies[file+":"+name] = ContentHash(body)
		}
	}
	if body, ok := result["target_body"].(string); ok && body != "" {
		if target, ok := result["target"].(map[string]any); ok {
			file, _ := target["file"].(string)
			name, _ := target["name"].(string)
			s.SeenBodies[file+":"+name] = ContentHash(body)
		}
	}
	if matchesAny, ok := result["matches"]; ok {
		if matches, ok := matchesAny.([]any); ok {
			for _, mAny := range matches {
				m, ok := mAny.(map[string]any)
				if !ok {
					continue
				}
				body, ok := m["body"].(string)
				if !ok || body == "" {
					continue
				}
				sym, _ := m["symbol"].(map[string]any)
				if sym == nil {
					continue
				}
				file, _ := sym["file"].(string)
				name, _ := sym["name"].(string)
				s.SeenBodies[file+":"+name] = ContentHash(body)
			}
		}
	}
}

func (s *Session) StripSeenBodies(result map[string]any, cmd string) {
	var skipped []string

	switch cmd {
	case "gather":
		if body, ok := result["target_body"].(string); ok && body != "" {
			if target, ok := result["target"].(map[string]any); ok {
				file, _ := target["file"].(string)
				name, _ := target["name"].(string)
				key := file + ":" + name
				h := ContentHash(body)
				if prev, exists := s.SeenBodies[key]; exists && prev == h {
					result["target_body"] = "[in context]"
					skipped = append(skipped, name)
				} else {
					s.SeenBodies[key] = h
				}
			}
		}
		s.stripSnippetMap(result, "caller_snippets", &skipped)
		s.stripSnippetMap(result, "test_snippets", &skipped)

	case "search":
		if matchesAny, ok := result["matches"]; ok {
			if matches, ok := matchesAny.([]any); ok {
				for _, mAny := range matches {
					m, ok := mAny.(map[string]any)
					if !ok {
						continue
					}
					body, ok := m["body"].(string)
					if !ok || body == "" {
						continue
					}
					sym, _ := m["symbol"].(map[string]any)
					if sym == nil {
						continue
					}
					file, _ := sym["file"].(string)
					name, _ := sym["name"].(string)
					key := file + ":" + name
					h := ContentHash(body)
					if prev, exists := s.SeenBodies[key]; exists && prev == h {
						m["body"] = "[in context]"
						skipped = append(skipped, name)
					} else {
						s.SeenBodies[key] = h
					}
				}
			}
		}
	}

	if len(skipped) > 0 {
		s.stats.BodyDedup += len(skipped)
		result["skipped_bodies"] = skipped
	}
}

func (s *Session) stripSnippetMap(result map[string]any, field string, skipped *[]string) {
	snippets, ok := result[field].(map[string]any)
	if !ok {
		return
	}
	for name, bodyAny := range snippets {
		body, ok := bodyAny.(string)
		if !ok || body == "" {
			continue
		}
		if s.isBodySeen(name, body) {
			snippets[name] = "[in context]"
			*skipped = append(*skipped, name)
		} else {
			s.trackBodyByName(name, body)
		}
	}
}

// IsBlockSeen checks if a block hash has been seen in this session.
func (s *Session) IsBlockSeen(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.SeenBodies[key]
	return ok
}

// MarkBlockSeen records a block hash as seen.
func (s *Session) MarkBlockSeen(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.SeenBodies[key] = "1"
}

func (s *Session) isBodySeen(name, body string) bool {
	h := ContentHash(body)
	for key, prevHash := range s.SeenBodies {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) == 2 && parts[1] == name && prevHash == h {
			return true
		}
	}
	return false
}

func (s *Session) trackBodyByName(name, body string) {
	h := ContentHash(body)
	for key := range s.SeenBodies {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) == 2 && parts[1] == name {
			s.SeenBodies[key] = h
			return
		}
	}
	s.SeenBodies[":"+name] = h
}

// --- Post-processing pipeline ---

// PostProcess applies all session-layer optimizations to a dispatch result.
func (s *Session) PostProcess(cmd string, args []string, flags map[string]any, result any, text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		return s.postProcessNonObject(cmd, args, flags, text)
	}

	// Level 1: Store edit diffs (always inline, no slim optimization)
	if DiffEditCommands[cmd] {
		s.storeDiff(m, flags)
		// Update FileHashes so the next edit uses the post-edit hash,
		// not the stale hash from the prior read.
		s.updateFileHashFromResult(m)
		if m["diff_available"] == true {
			data, _ := json.Marshal(m)
			return string(data)
		}
	}

	// Level 2: Delta reads
	if cmdspec.IsDeltaRead(cmd) {
		if delta := s.ProcessReadResult(cmd, m, flags); delta != nil {
			data, _ := json.Marshal(delta)
			return string(data)
		}
		// ProcessReadResult may have added "session" field to m
		if _, has := m["session"]; has {
			data, _ := json.Marshal(m)
			text = string(data)
		}
	}

	// Level 2b: Content-hash session tracking for search/map
	// Hash the visible payload and return "unchanged" if agent already has it.
	if cmd == "search" || cmd == "map" || cmd == "orient" {
		cacheKey := s.CacheKey(cmd, args, flags)
		status, _ := s.CheckContent(cacheKey, text, false)
		if status == "unchanged" {
			s.stats.DeltaReads++
			// Mark as unchanged but preserve the full body — search/map
			// results are already budget-capped and agents need to re-reference
			// results after context compression.
			m["session"] = "unchanged"
			data, _ := json.Marshal(m)
			return string(data)
		}
		s.StoreContent(cacheKey, text, false)
		m["session"] = "new"
		data, _ := json.Marshal(m)
		text = string(data)
	}

	// Level 3: Strip seen bodies from gather/search.
	willStrip := cmd == "gather" || (cmd == "search" && FlagIsTruthy(flags, "body"))
	if cmdspec.IsBodyTrack(cmd) && !willStrip {
		s.TrackBodies(m, cmd)
	}
	if willStrip {
		s.StripSeenBodies(m, cmd)
		data, _ := json.Marshal(m)
		return string(data)
	}

	return text
}

// PostProcessNonObject handles array results (batch read).
func (s *Session) PostProcessNonObject(cmd string, args []string, flags map[string]any, text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.postProcessNonObject(cmd, args, flags, text)
}

func (s *Session) postProcessNonObject(cmd string, args []string, flags map[string]any, text string) string {
	if cmd != "read" {
		return text
	}

	isFull := FlagIsTruthy(flags, "full")

	var entries []map[string]any
	if err := json.Unmarshal([]byte(text), &entries); err != nil {
		return text
	}

	modified := false
	for i, entry := range entries {
		content, ok := entry["content"].(string)
		if !ok || content == "" {
			continue
		}

		file, _ := entry["file"].(string)
		symbol, _ := entry["symbol"].(string)

		var key string
		var isSymbol bool
		if symbol != "" {
			key = file + ":" + symbol
			isSymbol = true
			s.SeenBodies[key] = ContentHash(content)
		} else {
			lines := entry["lines"]
			key = fmt.Sprintf("%s:%v", file, lines)
		}

		if isFull {
			s.StoreContent(key, content, isSymbol)
			continue
		}

		status, _ := s.CheckContent(key, content, isSymbol)
		switch status {
		case "new":
			s.StoreContent(key, content, isSymbol)
		case "unchanged":
			hash, _ := entry["hash"].(string)
			entries[i] = map[string]any{"unchanged": true, "file": file, "hash": hash}
			if symbol != "" {
				entries[i]["symbol"] = symbol
			}
			modified = true
		}
	}

	if modified {
		data, _ := json.Marshal(entries)
		return string(data)
	}
	return text
}

// FlagIsTruthy checks if a flag value is boolean true.
func FlagIsTruthy(flags map[string]any, key string) bool {
	v, ok := flags[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}
