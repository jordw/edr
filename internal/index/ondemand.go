package index

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// OnDemand implements SymbolStore by parsing files with tree-sitter on demand.
// Parses files with tree-sitter on demand. No pre-built index, no staleness.
type OnDemand struct {
	root   string
	edrDir string

	mu    sync.RWMutex
	cache map[string]*cachedFile // abs path -> parsed result
}

type cachedFile struct {
	mtime   int64
	hash    string
	symbols []SymbolInfo
	imports []ImportInfo
	src     []byte // raw file source for byte-range body access
}

// NewOnDemand creates an on-demand symbol store rooted at the given directory.
func NewOnDemand(root string) *OnDemand {
	if normalized, err := NormalizeRoot(root); err == nil {
		root = normalized
	}
	edrDir := HomeEdrDir(root)
	return &OnDemand{
		root:   root,
		edrDir: edrDir,
		cache:  make(map[string]*cachedFile),
	}
}

func (o *OnDemand) Root() string  { return o.root }
func (o *OnDemand) EdrDir() string { return o.edrDir }
func (o *OnDemand) Close() error   { return nil }

func (o *OnDemand) ResolvePath(path string) (string, error) {
	return ResolvePath(o.root, path)
}

func (o *OnDemand) ResolvePathReadOnly(path string) (string, error) {
	return ResolvePathReadOnly(o.root, path)
}

// parseFile parses a file and returns cached results, re-parsing if mtime changed.
func (o *OnDemand) parseFile(absPath string) (*cachedFile, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, err
	}
	mtime := info.ModTime().UnixMicro()

	o.mu.RLock()
	if cached, ok := o.cache[absPath]; ok && cached.mtime == mtime {
		o.mu.RUnlock()
		return cached, nil
	}
	o.mu.RUnlock()

	if !RegexSupported(absPath) {
		// Not a parseable file — return empty result
		return &cachedFile{mtime: mtime}, nil
	}

	src, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}

	syms := RegexParse(absPath, src)
	var imports []ImportInfo // regex parser doesn't extract imports

	h := sha256.Sum256(src)

	// Strip bodies — they account for ~80% of cache memory.
	// Callers that need body text read the file and slice by byte range.
	for i := range syms {
		syms[i].Body = ""
	}

	cf := &cachedFile{
		mtime:   mtime,
		hash:    hex.EncodeToString(h[:16]),
		symbols: syms,
		imports: imports,
		src:     src,
	}

	o.mu.Lock()
	o.cache[absPath] = cf
	o.mu.Unlock()

	return cf, nil
}

// absPath resolves a relative path to absolute.
func (o *OnDemand) absPath(relOrAbs string) string {
	if filepath.IsAbs(relOrAbs) {
		return relOrAbs
	}
	return filepath.Join(o.root, relOrAbs)
}

// --- Single-file operations ---

func (o *OnDemand) GetSymbol(ctx context.Context, file, name string) (*SymbolInfo, error) {
	cf, err := o.parseFile(o.absPath(file))
	if err != nil {
		return nil, err
	}
	// When multiple symbols share a name (e.g. TS overloads), prefer the
	// one with the largest span — that is the implementation, not a signature.
	var best *SymbolInfo
	for i := range cf.symbols {
		if cf.symbols[i].Name == name {
			s := &cf.symbols[i]
			if best == nil || (s.EndLine-s.StartLine) > (best.EndLine-best.StartLine) {
				best = s
			}
		}
	}
	if best != nil {
		return best, nil
	}
	return nil, o.symbolNotFoundError(ctx, name, file)
}

func (o *OnDemand) GetSymbolsByFile(ctx context.Context, file string) ([]SymbolInfo, error) {
	cf, err := o.parseFile(o.absPath(file))
	if err != nil {
		return nil, err
	}
	out := make([]SymbolInfo, len(cf.symbols))
	copy(out, cf.symbols)
	return out, nil
}

func (o *OnDemand) GetContainerAt(ctx context.Context, file string, line int) (*SymbolInfo, error) {
	cf, err := o.parseFile(o.absPath(file))
	if err != nil {
		return nil, err
	}
	var best *SymbolInfo
	for i := range cf.symbols {
		s := &cf.symbols[i]
		if s.Type != "class" && s.Type != "struct" && s.Type != "interface" &&
			s.Type != "module" && s.Type != "impl" && s.Type != "enum" &&
			s.Type != "trait" && s.Type != "object" {
			continue
		}
		if int(s.StartLine) <= line && line <= int(s.EndLine) {
			if best == nil || (s.StartLine >= best.StartLine && s.EndLine <= best.EndLine) {
				sym := *s
				best = &sym
			}
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no container at line %d", line)
	}
	return best, nil
}

func (o *OnDemand) GetFileHash(ctx context.Context, path string) (string, error) {
	cf, err := o.parseFile(o.absPath(path))
	if err != nil {
		return "", err
	}
	return cf.hash, nil
}

// --- Cross-file operations ---

// parseAll walks the repo and parses all files in parallel, streaming files
// directly into worker goroutines so parsing begins during the walk.
func (o *OnDemand) parseAll(ctx context.Context) map[string]*cachedFile {
	results := make(map[string]*cachedFile, 256)
	var mu sync.Mutex

	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}

	ch := make(chan string, workers*4)

	// Walk and parse concurrently: walk feeds paths into ch,
	// workers parse them as they arrive.
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range ch {
				if ctx.Err() != nil {
					return
				}
				cf, err := o.parseFile(path)
				if err != nil || cf == nil {
					continue
				}
				mu.Lock()
				results[path] = cf
				mu.Unlock()
			}
		}()
	}

	WalkRepoFiles(o.root, func(path string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if RegexSupported(path) {
			ch <- path
		}
		return nil
	})
	close(ch)
	wg.Wait()
	return results
}

// parseDir parses all files in a directory subtree, streaming into workers.
func (o *OnDemand) parseDir(ctx context.Context, dir string) map[string]*cachedFile {
	absDir := o.absPath(dir)
	results := make(map[string]*cachedFile, 64)
	var mu sync.Mutex

	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	ch := make(chan string, workers*4)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range ch {
				if ctx.Err() != nil {
					return
				}
				cf, err := o.parseFile(path)
				if err != nil || cf == nil {
					continue
				}
				mu.Lock()
				results[path] = cf
				mu.Unlock()
			}
		}()
	}

	WalkRepoFiles(o.root, func(path string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if strings.HasPrefix(path, absDir) && RegexSupported(path) {
			ch <- path
		}
		return nil
	})
	close(ch)
	wg.Wait()
	return results
}

func (o *OnDemand) ResolveSymbol(ctx context.Context, name string) (*SymbolInfo, error) {
	all := o.parseAll(ctx)
	var candidates []SymbolInfo
	for _, cf := range all {
		for _, s := range cf.symbols {
			if s.Name == name {
				candidates = append(candidates, s)
			}
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("symbol %q not found", name)
	}
	if len(candidates) == 1 {
		return &candidates[0], nil
	}
	// Prefer definitions over other types
	if best := preferDefinition(candidates); best != nil {
		return best, nil
	}
	return nil, &AmbiguousSymbolError{Name: name, Root: o.root, Candidates: candidates}
}

func (o *OnDemand) SearchSymbols(ctx context.Context, pattern string, limit ...int) ([]SymbolInfo, error) {
	all := o.parseAll(ctx)
	lim := 50
	if len(limit) > 0 && limit[0] > 0 {
		lim = limit[0]
	}

	lowerPattern := strings.ToLower(pattern)
	var results []SymbolInfo
	for _, cf := range all {
		for _, s := range cf.symbols {
			if strings.Contains(strings.ToLower(s.Name), lowerPattern) {
				results = append(results, s)
				if len(results) >= lim {
					return results, nil
				}
			}
		}
	}
	return results, nil
}

func (o *OnDemand) AllSymbols(ctx context.Context) ([]SymbolInfo, error) {
	all := o.parseAll(ctx)
	var results []SymbolInfo
	for _, cf := range all {
		results = append(results, cf.symbols...)
	}
	return results, nil
}

func (o *OnDemand) FilteredSymbols(ctx context.Context, dir, symbolType, namePattern string) ([]SymbolInfo, error) {
	// Always parse all files, then filter by dir prefix.
	// dir may be absolute (from RepoMap) or relative.
	parsed := o.parseAll(ctx)

	// Normalize dir to absolute for comparison with symbol file paths.
	absDir := ""
	if dir != "" {
		if filepath.IsAbs(dir) {
			absDir = dir
		} else {
			absDir = filepath.Join(o.root, dir)
		}
	}

	lowerPattern := strings.ToLower(namePattern)
	var results []SymbolInfo
	for _, cf := range parsed {
		for _, s := range cf.symbols {
			if absDir != "" && !strings.HasPrefix(s.File, absDir) {
				continue
			}
			if symbolType != "" && s.Type != symbolType {
				continue
			}
			if namePattern != "" && !strings.Contains(strings.ToLower(s.Name), lowerPattern) {
				continue
			}
			results = append(results, s)
		}
	}
	return results, nil
}

// --- Cross-file references ---

func (o *OnDemand) FindSemanticCallers(ctx context.Context, symbolName, symbolFile string) ([]SymbolInfo, error) {
	all := o.parseAll(ctx)

	nameBytes := []byte(symbolName)
	var callers []SymbolInfo
	for _, cf := range all {
		if len(cf.symbols) == 0 || len(cf.src) == 0 {
			continue
		}
		// Quick whole-file check before anything else
		if !bytes.Contains(cf.src, nameBytes) {
			continue
		}
		// Check import visibility
		sameFile := cf.symbols[0].File == symbolFile
		if !sameFile && !importsReach(cf.imports, symbolFile, cf.symbols[0].File, o.root) {
			continue
		}
		for _, s := range cf.symbols {
			if s.StartByte >= s.EndByte || int(s.EndByte) > len(cf.src) {
				continue
			}
			if bytes.Contains(cf.src[s.StartByte:s.EndByte], nameBytes) {
				callers = append(callers, s)
			}
		}
	}
	return callers, nil
}

func (o *OnDemand) FindSameFileCallers(ctx context.Context, symbolName, symbolFile string) ([]SymbolInfo, error) {
	cf, err := o.parseFile(o.absPath(symbolFile))
	if err != nil {
		return nil, err
	}
	nameBytes := []byte(symbolName)
	var callers []SymbolInfo
	for _, s := range cf.symbols {
		if s.Name == symbolName {
			continue
		}
		if s.StartByte < s.EndByte && int(s.EndByte) <= len(cf.src) {
			if bytes.Contains(cf.src[s.StartByte:s.EndByte], nameBytes) {
				callers = append(callers, s)
			}
		}
	}
	return callers, nil
}

func (o *OnDemand) FindSemanticReferences(ctx context.Context, symbolName, symbolFile string) ([]SymbolInfo, error) {
	// Same as FindSemanticCallers for on-demand — we find symbols whose body
	// contains the name and whose file imports the target.
	return o.FindSemanticCallers(ctx, symbolName, symbolFile)
}

func (o *OnDemand) HasRefs(_ context.Context) bool {
	return true // always available on-demand
}

// --- Metadata ---

func (o *OnDemand) Stats(ctx context.Context) (files int, symbols int, err error) {
	var fileCount, symCount int
	WalkRepoFiles(o.root, func(path string) error {
		if RegexSupported(path) {
			fileCount++
		}
		return nil
	})
	// Only count symbols if we've parsed some files
	o.mu.RLock()
	for _, cf := range o.cache {
		symCount += len(cf.symbols)
	}
	o.mu.RUnlock()
	return fileCount, symCount, nil
}

func (o *OnDemand) IndexWarnings() []FileError { return nil }

// --- Mutation ---

func (o *OnDemand) InvalidateFiles(_ context.Context, paths []string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, p := range paths {
		delete(o.cache, o.absPath(p))
	}
	return nil
}

func (o *OnDemand) WithWriteLock(fn func() error) error {
	return fn()
}

// --- Error helpers ---

func (o *OnDemand) symbolNotFoundError(_ context.Context, name, file string) error {
	cf, err := o.parseFile(o.absPath(file))
	if err != nil {
		return fmt.Errorf("symbol %q not found in %s", name, file)
	}
	var available []string
	for _, s := range cf.symbols {
		available = append(available, s.Name)
	}
	if len(available) == 0 {
		return fmt.Errorf("symbol %q not found in %s (no symbols found)", name, file)
	}

	// Find closest match
	best := ""
	bestDist := len(name) + 1
	for _, a := range available {
		d := levenshtein(strings.ToLower(name), strings.ToLower(a))
		if d < bestDist {
			bestDist = d
			best = a
		}
	}
	if best != "" && bestDist <= 3 {
		return fmt.Errorf("symbol %q not found in %s — did you mean %q?", name, file, best)
	}
	return fmt.Errorf("symbol %q not found in %s", name, file)
}

// levenshtein computes edit distance between two strings.
func levenshtein(a, b string) int {
	if len(a) == 0 { return len(b) }
	if len(b) == 0 { return len(a) }
	prev := make([]int, len(b)+1)
	for j := range prev { prev[j] = j }
	for i := 1; i <= len(a); i++ {
		curr := make([]int, len(b)+1)
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] { cost = 0 }
			curr[j] = min(curr[j-1]+1, min(prev[j]+1, prev[j-1]+cost))
		}
		prev = curr
	}
	return prev[len(b)]
}
