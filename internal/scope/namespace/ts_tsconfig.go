package namespace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// tsConfigPaths caches the parsed tsconfig.json reachable from a given
// directory. Callers get the effective baseDir (tsconfigDir/baseUrl or
// tsconfigDir when baseUrl is absent) plus the paths map.
//
// Parsing is lenient to the flavors of JSON tsconfig accepts:
//   - // line comments
//   - /* block comments */
//   - trailing commas
//
// Neither is stripped from inside string literals.
type tsConfigPaths struct {
	mu    sync.Mutex
	byDir map[string]*tsConfigInfo // dir → info (nil ⇒ no tsconfig above)
}

type tsConfigInfo struct {
	BaseDir string              // absolute directory that path patterns resolve under
	Paths   map[string][]string // pattern → targets (raw as in tsconfig)
}

func newTSConfigPaths() *tsConfigPaths {
	return &tsConfigPaths{byDir: make(map[string]*tsConfigInfo)}
}

// ConfigForFile walks up from file's directory looking for a
// tsconfig.json. Returns nil when none is found or when the config
// has no `paths` mapping.
func (c *tsConfigPaths) ConfigForFile(file string) *tsConfigInfo {
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil
	}
	return c.configForDir(filepath.Dir(abs))
}

func (c *tsConfigPaths) configForDir(dir string) *tsConfigInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	if info, ok := c.byDir[dir]; ok {
		return info
	}
	var chain []string
	cur := dir
	for {
		chain = append(chain, cur)
		if info, ok := c.byDir[cur]; ok {
			for _, d := range chain {
				c.byDir[d] = info
			}
			return info
		}
		cfg := filepath.Join(cur, "tsconfig.json")
		if _, err := os.Stat(cfg); err == nil {
			info := readTSConfig(cur, cfg)
			for _, d := range chain {
				c.byDir[d] = info
			}
			return info
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			for _, d := range chain {
				c.byDir[d] = nil
			}
			return nil
		}
		cur = parent
	}
}

// readTSConfig reads + parses a tsconfig.json file. Returns nil when
// the file can't be parsed or contains no `paths` mapping.
func readTSConfig(dir, path string) *tsConfigInfo {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	cleaned := stripJSONC(raw)
	var cfg struct {
		CompilerOptions struct {
			BaseURL string              `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if err := json.Unmarshal(cleaned, &cfg); err != nil {
		return nil
	}
	if len(cfg.CompilerOptions.Paths) == 0 {
		return nil
	}
	baseDir := dir
	if cfg.CompilerOptions.BaseURL != "" {
		baseDir = filepath.Join(dir, cfg.CompilerOptions.BaseURL)
	}
	return &tsConfigInfo{BaseDir: baseDir, Paths: cfg.CompilerOptions.Paths}
}

// stripJSONC removes // line comments, /* block comments */, and
// trailing commas from a JSONC blob, ignoring occurrences inside
// double-quoted string literals. Good enough for tsconfig.json;
// not a full JSONC spec implementation.
func stripJSONC(src []byte) []byte {
	out := make([]byte, 0, len(src))
	i := 0
	inString := false
	for i < len(src) {
		c := src[i]
		if inString {
			out = append(out, c)
			if c == '\\' && i+1 < len(src) {
				out = append(out, src[i+1])
				i += 2
				continue
			}
			if c == '"' {
				inString = false
			}
			i++
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			i++
			continue
		}
		if c == '/' && i+1 < len(src) {
			if src[i+1] == '/' {
				// Line comment.
				for i < len(src) && src[i] != '\n' {
					i++
				}
				continue
			}
			if src[i+1] == '*' {
				// Block comment.
				i += 2
				for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
					i++
				}
				if i+1 < len(src) {
					i += 2
				}
				continue
			}
		}
		out = append(out, c)
		i++
	}
	// Strip trailing commas before } or ].
	stripped := make([]byte, 0, len(out))
	for i := 0; i < len(out); i++ {
		if out[i] == ',' {
			j := i + 1
			for j < len(out) && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
				j++
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				continue // skip the comma
			}
		}
		stripped = append(stripped, out[i])
	}
	return stripped
}

// Resolve tries each tsconfig path pattern against importSpec. On
// match, returns the list of resolved filesystem paths (usually
// one). Patterns may contain a single `*` wildcard.
//
// A tsconfig pattern `@/*` with target `./*` and baseDir `/repo/src`
// maps `@/components/Foo` → `/repo/src/components/Foo`.
func (info *tsConfigInfo) Resolve(importSpec string) []string {
	if info == nil {
		return nil
	}
	var out []string
	for pattern, targets := range info.Paths {
		capture, ok := matchTSPathPattern(pattern, importSpec)
		if !ok {
			continue
		}
		for _, target := range targets {
			resolved := strings.Replace(target, "*", capture, 1)
			out = append(out, filepath.Join(info.BaseDir, resolved))
		}
	}
	return out
}

// matchTSPathPattern matches importSpec against pattern. If pattern
// contains `*`, the matched substring is returned via capture.
// Exact-match patterns (no `*`) match only when importSpec == pattern.
func matchTSPathPattern(pattern, spec string) (string, bool) {
	idx := strings.Index(pattern, "*")
	if idx < 0 {
		if pattern == spec {
			return "", true
		}
		return "", false
	}
	prefix := pattern[:idx]
	suffix := pattern[idx+1:]
	if !strings.HasPrefix(spec, prefix) {
		return "", false
	}
	if !strings.HasSuffix(spec, suffix) {
		return "", false
	}
	if len(spec) < len(prefix)+len(suffix) {
		return "", false
	}
	return spec[len(prefix) : len(spec)-len(suffix)], true
}
