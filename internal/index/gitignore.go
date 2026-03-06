package index

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// GitIgnoreMatcher matches file paths against .gitignore patterns.
type GitIgnoreMatcher struct {
	patterns []ignorePattern
}

type ignorePattern struct {
	pattern  string
	negated  bool
	dirOnly  bool
	anchored bool // contains / so matches full path, not just basename
}

// LoadGitIgnore reads a .gitignore file and returns a matcher.
// Returns nil (not an error) if the file doesn't exist.
func LoadGitIgnore(repoRoot string) *GitIgnoreMatcher {
	path := filepath.Join(repoRoot, ".gitignore")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	m := &GitIgnoreMatcher{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		p := ignorePattern{}
		if strings.HasPrefix(line, "!") {
			p.negated = true
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			p.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		// Patterns with / (other than trailing) are anchored to root
		if strings.Contains(line, "/") {
			p.anchored = true
			line = strings.TrimPrefix(line, "/")
		}
		p.pattern = line
		m.patterns = append(m.patterns, p)
	}
	return m
}

// IsIgnored returns true if the given relative path should be ignored.
func (m *GitIgnoreMatcher) IsIgnored(relPath string, isDir bool) bool {
	if m == nil {
		return false
	}

	ignored := false
	for _, p := range m.patterns {
		if p.dirOnly && !isDir {
			continue
		}
		if matchPattern(relPath, p.pattern, p.anchored) {
			ignored = !p.negated
		}
	}
	return ignored
}

func matchPattern(path, pattern string, anchored bool) bool {
	// Handle ** patterns
	if strings.Contains(pattern, "**") {
		parts := strings.SplitN(pattern, "**", 2)
		prefix := strings.TrimSuffix(parts[0], "/")
		suffix := strings.TrimPrefix(parts[1], "/")

		if prefix != "" && !strings.HasPrefix(path, prefix+"/") && path != prefix {
			return false
		}
		if suffix == "" {
			return true
		}
		// Match suffix against basename and every subpath
		if ok, _ := filepath.Match(suffix, filepath.Base(path)); ok {
			return true
		}
		for i := 0; i < len(path); i++ {
			if path[i] == '/' {
				if ok, _ := filepath.Match(suffix, path[i+1:]); ok {
					return true
				}
			}
		}
		return false
	}

	if anchored {
		ok, _ := filepath.Match(pattern, path)
		return ok
	}

	// Unanchored: match against basename and full path
	if ok, _ := filepath.Match(pattern, filepath.Base(path)); ok {
		return true
	}
	if ok, _ := filepath.Match(pattern, path); ok {
		return true
	}
	return false
}
