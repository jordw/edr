// Package session provides context-aware response optimization for edr.
// It tracks what content the caller has already seen and produces deltas,
// slim edits, and body dedup — the same logic for MCP, CLI, and batch.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// ContentEntry tracks previously sent content for delta reads.
type ContentEntry struct {
	Hash    string `json:"hash"`
	Content string `json:"content"`
	Order   int    `json:"order"`
}

// Session tracks content the caller has already seen.
// It powers four optimizations:
//  1. Response-level dedup: identical read → {cached:true}
//  2. Slim edits: strip large diffs, serve via GetDiff
//  3. Delta reads: re-reads return a diff from the last-seen version
//  4. Seen-body stripping: gather/search skip bodies already in context
type Session struct {
	Responses     map[string]string       `json:"responses"`
	Diffs         map[string]string       `json:"diffs"`
	FileContent   map[string]ContentEntry `json:"file_content"`
	SymbolContent map[string]ContentEntry `json:"symbol_content"`
	ContentOrder  int                     `json:"content_order"`
	SeenBodies    map[string]string       `json:"seen_bodies"`

	// filePath is set for file-backed sessions. Empty = in-memory only.
	filePath string
}

const MaxContentEntries = 200

// New creates an in-memory session (for MCP and batch).
func New() *Session {
	return &Session{
		Responses:     make(map[string]string),
		Diffs:         make(map[string]string),
		FileContent:   make(map[string]ContentEntry),
		SymbolContent: make(map[string]ContentEntry),
		SeenBodies:    make(map[string]string),
	}
}

// Command category maps.
var ReadCommands = map[string]bool{
	"read-file": true, "read-symbol": true, "symbols": true,
	"expand": true, "gather": true, "batch-read": true,
	"repo-map": true, "search": true, "search-text": true,
	"xrefs": true, "find-files": true,
}

var EditCommands = map[string]bool{
	"smart-edit": true, "replace-text": true, "replace-symbol": true,
	"replace-lines": true, "replace-span": true, "write-file": true,
	"append-file": true, "insert-after": true, "rename-symbol": true,
	"edit-plan": true,
}

var DiffEditCommands = map[string]bool{
	"smart-edit": true,
}

var DeltaReadCommands = map[string]bool{
	"read-file": true, "read-symbol": true, "expand": true, "batch-read": true,
}

var BodyCommands = map[string]bool{
	"read-symbol": true, "expand": true, "gather": true,
	"search": true, "batch-read": true,
}

// --- Hashing & keys ---

func ContentHash(data string) string {
	h := sha256.Sum256([]byte(data))
	return hex.EncodeToString(h[:16])
}

func (s *Session) CacheKey(cmd string, args []string, flags map[string]any) string {
	key := cmd + "\x00" + strings.Join(args, "\x00")
	for _, f := range []string{"budget", "body", "callers", "deps", "signatures", "context", "regex", "include", "exclude", "dir", "glob", "type", "grep", "symbols", "full", "verbose"} {
		if v, ok := flags[f]; ok {
			key += fmt.Sprintf("\x00%s=%v", f, v)
		}
	}
	return key
}

// Check returns true if this response was already sent identically.
func (s *Session) Check(key, responseText string) bool {
	h := ContentHash(responseText)
	if prev, ok := s.Responses[key]; ok && prev == h {
		return true
	}
	s.Responses[key] = h
	return false
}

// --- Cache invalidation ---

func (s *Session) InvalidateFile(file string) {
	for k := range s.Responses {
		if strings.Contains(k, file) {
			delete(s.Responses, k)
		}
	}
}

func (s *Session) InvalidateForEdit(cmd string, args []string) {
	if cmd == "rename-symbol" || cmd == "init" {
		s.Responses = make(map[string]string)
		s.Diffs = make(map[string]string)
		s.FileContent = make(map[string]ContentEntry)
		s.SymbolContent = make(map[string]ContentEntry)
		s.SeenBodies = make(map[string]string)
		return
	}
	if len(args) > 0 {
		s.InvalidateFile(args[0])
	}
}

// --- Level 1: Slim edit responses ---

func CountDiffLines(diff string) int {
	count := 0
	for _, line := range strings.Split(diff, "\n") {
		if len(line) == 0 {
			continue
		}
		if (line[0] == '+' || line[0] == '-') &&
			!strings.HasPrefix(line, "---") && !strings.HasPrefix(line, "+++") {
			count++
		}
	}
	return count
}

// StoreDiff stores the diff from an edit result and returns a slimmed version.
// Small diffs (<=20 changed lines) are included inline. Large diffs are stored
// and available via GetDiff. Returns nil if verbose is set.
func (s *Session) StoreDiff(result map[string]any, flags map[string]any) map[string]any {
	diff, ok := result["diff"].(string)
	if !ok || diff == "" {
		return nil
	}
	if FlagIsTruthy(flags, "verbose") {
		return nil
	}

	file, _ := result["file"].(string)
	key := file
	if sym, ok := result["symbol"].(string); ok && sym != "" {
		key = file + ":" + sym
	}
	s.Diffs[key] = diff

	changedLines := CountDiffLines(diff)

	if changedLines <= 20 {
		result["lines_changed"] = changedLines
		result["diff_available"] = true
		return nil
	}

	slim := make(map[string]any)
	for k, v := range result {
		if k == "diff" || k == "old_size" || k == "new_size" {
			continue
		}
		slim[k] = v
	}
	slim["lines_changed"] = changedLines
	slim["diff_available"] = true
	return slim
}

// GetDiff returns a stored diff by file or file:symbol key.
func (s *Session) GetDiff(args []string) map[string]any {
	if len(args) == 0 {
		return map[string]any{"error": "get-diff requires 1-2 arguments: <file> [symbol]"}
	}
	key := args[0]
	if len(args) > 1 {
		key = args[0] + ":" + args[1]
	}
	if diff, ok := s.Diffs[key]; ok {
		return map[string]any{"diff": diff, "file": args[0]}
	}
	if len(args) > 1 {
		if diff, ok := s.Diffs[args[0]]; ok {
			return map[string]any{"diff": diff, "file": args[0]}
		}
	}
	return map[string]any{"error": "no diff stored", "key": key}
}

// --- Level 2: Delta reads ---

func (s *Session) evictLRU() {
	total := len(s.FileContent) + len(s.SymbolContent)
	if total <= MaxContentEntries {
		return
	}
	oldestKey := ""
	oldestOrder := s.ContentOrder + 1
	oldestIsFile := true

	for k, v := range s.FileContent {
		if v.Order < oldestOrder {
			oldestOrder = v.Order
			oldestKey = k
			oldestIsFile = true
		}
	}
	for k, v := range s.SymbolContent {
		if v.Order < oldestOrder {
			oldestOrder = v.Order
			oldestKey = k
			oldestIsFile = false
		}
	}
	if oldestKey != "" {
		if oldestIsFile {
			delete(s.FileContent, oldestKey)
		} else {
			delete(s.SymbolContent, oldestKey)
		}
	}
}

func (s *Session) StoreContent(key string, content string, isSymbol bool) {
	s.ContentOrder++
	entry := ContentEntry{
		Hash:    ContentHash(content),
		Content: content,
		Order:   s.ContentOrder,
	}
	if isSymbol {
		s.SymbolContent[key] = entry
	} else {
		s.FileContent[key] = entry
	}
	s.evictLRU()
}

// CheckContent checks if content has been seen before.
// Returns: "new", "unchanged", or "changed" (with old content and previous hash).
func (s *Session) CheckContent(key string, content string, isSymbol bool) (status string, oldContent string, prevHash string) {
	var store map[string]ContentEntry
	if isSymbol {
		store = s.SymbolContent
	} else {
		store = s.FileContent
	}

	prev, exists := store[key]
	if !exists {
		return "new", "", ""
	}

	h := ContentHash(content)
	if prev.Hash == h {
		s.ContentOrder++
		prev.Order = s.ContentOrder
		store[key] = prev
		return "unchanged", "", prev.Hash
	}
	return "changed", prev.Content, prev.Hash
}

// --- Text diff ---

func ComputeTextDiff(oldText, newText, label string) string {
	if oldText == newText {
		return ""
	}
	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")
	if len(oldLines) > 2000 || len(newLines) > 2000 {
		return ""
	}

	lcs := lcsLines(oldLines, newLines)
	hunks := buildHunks(oldLines, newLines, lcs, 3)
	if len(hunks) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n", label)
	fmt.Fprintf(&b, "+++ b/%s\n", label)
	for _, h := range hunks {
		b.WriteString(h)
	}
	return b.String()
}

type lcsMatch struct {
	oldIdx int
	newIdx int
}

func lcsLines(old, new []string) []lcsMatch {
	m, n := len(old), len(new)
	if m == 0 || n == 0 {
		return nil
	}
	if m*n > 1000000 {
		return lcsSimple(old, new)
	}

	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if old[i-1] == new[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	var result []lcsMatch
	i, j := m, n
	for i > 0 && j > 0 {
		if old[i-1] == new[j-1] {
			result = append(result, lcsMatch{i - 1, j - 1})
			i--
			j--
		} else if dp[i-1][j] >= dp[i][j-1] {
			i--
		} else {
			j--
		}
	}
	for l, r := 0, len(result)-1; l < r; l, r = l+1, r-1 {
		result[l], result[r] = result[r], result[l]
	}
	return result
}

func lcsSimple(old, new []string) []lcsMatch {
	var result []lcsMatch
	m, n := len(old), len(new)

	prefix := 0
	for prefix < m && prefix < n && old[prefix] == new[prefix] {
		result = append(result, lcsMatch{prefix, prefix})
		prefix++
	}

	suffix := 0
	for suffix < m-prefix && suffix < n-prefix && old[m-1-suffix] == new[n-1-suffix] {
		suffix++
	}

	if prefix < m-suffix && prefix < n-suffix {
		middle := prefix
		oi, ni := prefix, prefix
		for oi < m-suffix && ni < n-suffix {
			if old[oi] == new[ni] {
				result = append(result, lcsMatch{oi, ni})
				oi++
				ni++
			} else {
				_ = middle
				ni++
				if ni >= n-suffix {
					oi++
					ni = prefix
				}
			}
		}
	}

	for i := 0; i < suffix; i++ {
		result = append(result, lcsMatch{m - suffix + i, n - suffix + i})
	}
	return result
}

type diffLine struct {
	prefix byte
	text   string
}

func buildHunks(old, new []string, lcs []lcsMatch, contextLines int) []string {
	var allLines []diffLine
	type linePos struct {
		oldIdx int
		newIdx int
	}
	var positions []linePos

	oi, ni := 0, 0
	for _, m := range lcs {
		for oi < m.oldIdx {
			allLines = append(allLines, diffLine{'-', old[oi]})
			positions = append(positions, linePos{oi, ni})
			oi++
		}
		for ni < m.newIdx {
			allLines = append(allLines, diffLine{'+', new[ni]})
			positions = append(positions, linePos{oi, ni})
			ni++
		}
		allLines = append(allLines, diffLine{' ', old[oi]})
		positions = append(positions, linePos{oi, ni})
		oi++
		ni++
	}
	for oi < len(old) {
		allLines = append(allLines, diffLine{'-', old[oi]})
		positions = append(positions, linePos{oi, ni})
		oi++
	}
	for ni < len(new) {
		allLines = append(allLines, diffLine{'+', new[ni]})
		positions = append(positions, linePos{oi, ni})
		ni++
	}

	if len(allLines) == 0 {
		return nil
	}

	hasChanges := false
	for _, dl := range allLines {
		if dl.prefix != ' ' {
			hasChanges = true
			break
		}
	}
	if !hasChanges {
		return nil
	}

	var hunks []string
	inHunk := false
	hunkStart := 0
	lastChange := -contextLines - 1

	for i, dl := range allLines {
		if dl.prefix != ' ' {
			if !inHunk {
				hunkStart = i - contextLines
				if hunkStart < 0 {
					hunkStart = 0
				}
				inHunk = true
			}
			lastChange = i
		} else if inHunk && i-lastChange > contextLines {
			hunkEnd := lastChange + contextLines + 1
			if hunkEnd > len(allLines) {
				hunkEnd = len(allLines)
			}
			hunks = append(hunks, formatHunk(allLines[hunkStart:hunkEnd], positions[hunkStart].oldIdx, positions[hunkStart].newIdx))
			inHunk = false
		}
	}
	if inHunk {
		hunkEnd := lastChange + contextLines + 1
		if hunkEnd > len(allLines) {
			hunkEnd = len(allLines)
		}
		hunks = append(hunks, formatHunk(allLines[hunkStart:hunkEnd], positions[hunkStart].oldIdx, positions[hunkStart].newIdx))
	}

	return hunks
}

func formatHunk(lines []diffLine, oldStart, newStart int) string {
	var b strings.Builder
	oldCount, newCount := 0, 0
	for _, dl := range lines {
		if dl.prefix != '+' {
			oldCount++
		}
		if dl.prefix != '-' {
			newCount++
		}
	}
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldStart+1, oldCount, newStart+1, newCount)
	for _, dl := range lines {
		fmt.Fprintf(&b, "%c%s\n", dl.prefix, dl.text)
	}
	return b.String()
}

// --- Level 2: Process read results ---

func (s *Session) ProcessReadResult(cmd string, result map[string]any, flags map[string]any) map[string]any {
	if FlagIsTruthy(flags, "full") {
		s.StoreReadContent(cmd, result)
		return nil
	}

	var content, key, label string
	var isSymbol bool

	switch cmd {
	case "read-file":
		c, ok := result["content"].(string)
		if !ok || c == "" {
			return nil
		}
		content = c
		file, _ := result["file"].(string)
		lines, _ := result["lines"]
		key = fmt.Sprintf("%s:%v", file, lines)
		label = file

	case "read-symbol":
		c, ok := result["body"].(string)
		if !ok || c == "" {
			return nil
		}
		content = c
		isSymbol = true
		sym, _ := result["symbol"].(map[string]any)
		if sym == nil {
			return nil
		}
		file, _ := sym["file"].(string)
		name, _ := sym["name"].(string)
		key = file + ":" + name
		label = key
		s.SeenBodies[key] = ContentHash(content)

	case "expand":
		c, ok := result["body"].(string)
		if !ok || c == "" {
			return nil
		}
		content = c
		isSymbol = true
		sym, _ := result["symbol"].(map[string]any)
		if sym == nil {
			return nil
		}
		file, _ := sym["file"].(string)
		name, _ := sym["name"].(string)
		key = file + ":" + name
		label = key
		s.SeenBodies[key] = ContentHash(content)

	default:
		return nil
	}

	status, oldContent, prevHash := s.CheckContent(key, content, isSymbol)

	switch status {
	case "new":
		s.StoreContent(key, content, isSymbol)
		return nil

	case "unchanged":
		file, hash := ExtractFileHash(result)
		return map[string]any{"unchanged": true, "file": file, "hash": hash}

	case "changed":
		diff := ComputeTextDiff(oldContent, content, label)
		s.StoreContent(key, content, isSymbol)
		if diff == "" {
			return nil
		}
		file, hash := ExtractFileHash(result)
		return map[string]any{
			"delta":         true,
			"file":          file,
			"diff":          diff,
			"hash":          hash,
			"previous_hash": prevHash,
			"new_size":      len(content) / 4,
		}
	}
	return nil
}

// ExtractFileHash gets file and hash from a result map.
func ExtractFileHash(result map[string]any) (file, hash string) {
	file, _ = result["file"].(string)
	hash, _ = result["hash"].(string)
	if file == "" {
		if sym, ok := result["symbol"].(map[string]any); ok {
			file, _ = sym["file"].(string)
			hash, _ = sym["hash"].(string)
		}
	}
	return
}

// StoreReadContent stores content from a read result for future delta tracking.
func (s *Session) StoreReadContent(cmd string, result map[string]any) {
	switch cmd {
	case "read-file":
		if c, ok := result["content"].(string); ok && c != "" {
			file, _ := result["file"].(string)
			lines, _ := result["lines"]
			key := fmt.Sprintf("%s:%v", file, lines)
			s.StoreContent(key, c, false)
		}
	case "read-symbol", "expand":
		if c, ok := result["body"].(string); ok && c != "" {
			sym, _ := result["symbol"].(map[string]any)
			if sym != nil {
				file, _ := sym["file"].(string)
				name, _ := sym["name"].(string)
				key := file + ":" + name
				s.StoreContent(key, c, true)
				s.SeenBodies[key] = ContentHash(c)
			}
		}
	}
}

// --- Level 3: Body tracking ---

func (s *Session) TrackBodies(result map[string]any, cmd string) {
	switch cmd {
	case "read-symbol", "expand":
		if body, ok := result["body"].(string); ok && body != "" {
			sym, _ := result["symbol"].(map[string]any)
			if sym != nil {
				file, _ := sym["file"].(string)
				name, _ := sym["name"].(string)
				s.SeenBodies[file+":"+name] = ContentHash(body)
			}
		}
	case "gather":
		if body, ok := result["target_body"].(string); ok && body != "" {
			if target, ok := result["target"].(map[string]any); ok {
				file, _ := target["file"].(string)
				name, _ := target["name"].(string)
				s.SeenBodies[file+":"+name] = ContentHash(body)
			}
		}
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
					s.SeenBodies[file+":"+name] = ContentHash(body)
				}
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
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		return s.PostProcessNonObject(cmd, args, flags, text)
	}

	// Level 1: Slim edit responses
	if DiffEditCommands[cmd] {
		if slim := s.StoreDiff(m, flags); slim != nil {
			data, _ := json.Marshal(slim)
			return string(data)
		} else if m["diff_available"] == true {
			data, _ := json.Marshal(m)
			return string(data)
		}
	}

	// Level 2: Delta reads
	if DeltaReadCommands[cmd] && cmd != "batch-read" {
		if delta := s.ProcessReadResult(cmd, m, flags); delta != nil {
			data, _ := json.Marshal(delta)
			return string(data)
		}
	}

	// Level 3: Track seen bodies
	if BodyCommands[cmd] && cmd != "batch-read" {
		s.TrackBodies(m, cmd)
	}

	// Level 3: Strip seen bodies from gather/search
	if cmd == "gather" || (cmd == "search" && FlagIsTruthy(flags, "body")) {
		s.StripSeenBodies(m, cmd)
		data, _ := json.Marshal(m)
		return string(data)
	}

	return text
}

// PostProcessNonObject handles array results (batch-read).
func (s *Session) PostProcessNonObject(cmd string, args []string, flags map[string]any, text string) string {
	if cmd != "batch-read" {
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
			lines, _ := entry["lines"]
			key = fmt.Sprintf("%s:%v", file, lines)
		}

		if isFull {
			s.StoreContent(key, content, isSymbol)
			continue
		}

		status, oldContent, prevHash := s.CheckContent(key, content, isSymbol)
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
		case "changed":
			label := file
			if symbol != "" {
				label = file + ":" + symbol
			}
			diff := ComputeTextDiff(oldContent, content, label)
			s.StoreContent(key, content, isSymbol)
			if diff != "" {
				entries[i] = map[string]any{
					"delta":         true,
					"file":          file,
					"diff":          diff,
					"hash":          entry["hash"],
					"previous_hash": prevHash,
					"new_size":      len(content) / 4,
				}
				if symbol != "" {
					entries[i]["symbol"] = symbol
				}
				modified = true
			}
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
