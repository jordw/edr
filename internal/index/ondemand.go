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

	"github.com/jordw/edr/internal/idx"
)

// OnDemand implements SymbolStore by parsing files with tree-sitter on demand.
// Parses files with tree-sitter on demand. No pre-built index, no staleness.
type OnDemand struct {
	root   string
	edrDir string

	mu        sync.RWMutex
	cache     map[string]*cachedFile // abs path -> parsed result
	fileCount int                     // cached file count from Stats, -1 = unset
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
		root:      root,
		edrDir:    edrDir,
		cache:     make(map[string]*cachedFile),
		fileCount: -1,
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

	if !Supported(absPath) {
		// Not a parseable file — return empty result
		return &cachedFile{mtime: mtime}, nil
	}

	src, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}

	var syms []SymbolInfo
	var imports []ImportInfo
	switch strings.ToLower(filepath.Ext(absPath)) {
	case ".rb":
		r := ParseRuby(src)
		syms = rubyToSymbolInfo(absPath, src, r)
		imports = rubyImportsToInfo(absPath, r)
	case ".js", ".jsx", ".ts", ".tsx", ".mts", ".cts":
		r := ParseTS(src)
		syms = tsToSymbolInfo(absPath, src, r)
		imports = tsImportsToInfo(absPath, r)
	case ".go":
		r := ParseGo(src)
		syms = goToSymbolInfo(absPath, src, r)
		imports = goImportsToInfo(absPath, r)
	case ".py", ".pyi":
		r := ParsePython(src)
		syms = pythonToSymbolInfo(absPath, src, r)
		imports = pyImportsToInfo(absPath, r)
	case ".java":
		r := ParseJava(src)
		syms = javaToSymbolInfo(absPath, src, r)
		imports = javaImportsToInfo(absPath, r)
	case ".cs":
		r := ParseCSharp(src)
		syms = csharpToSymbolInfo(absPath, src, r)
		imports = csharpImportsToInfo(absPath, r)
	case ".rs":
		r := ParseRust(src)
		syms = rustToSymbolInfo(absPath, src, r)
		imports = rustImportsToInfo(absPath, r)
	case ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hxx", ".hh":
		r := ParseCpp(src)
		syms = cppToSymbolInfo(absPath, src, r)
		imports = cppImportsToInfo(absPath, r)
	case ".kt", ".kts":
		r := ParseKotlin(src)
		syms = kotlinToSymbolInfo(absPath, src, r)
		imports = kotlinImportsToInfo(absPath, r)
	case ".swift":
		r := ParseSwift(src)
		syms = swiftToSymbolInfo(absPath, src, r)
		imports = swiftImportsToInfo(absPath, r)
	case ".scala", ".sc":
		r := ParseScala(src)
		syms = scalaToSymbolInfo(absPath, src, r)
		imports = scalaImportsToInfo(absPath, r)
	case ".php":
		r := ParsePHP(src)
		syms = phpToSymbolInfo(absPath, src, r)
		imports = phpImportsToInfo(absPath, r)
	case ".lua":
		r := ParseLua(src)
		syms = luaToSymbolInfo(absPath, src, r)
		imports = luaImportsToInfo(absPath, r)
	case ".zig":
		r := ParseZig(src)
		syms = zigToSymbolInfo(absPath, src, r)
		imports = zigImportsToInfo(absPath, r)
	}

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
	// When multiple symbols share a name, prefer:
	// 1. Definitions (struct/class/enum/type/interface) over impls/methods
	// 2. Among same kind, the largest span (implementation over overload signature)
	var best *SymbolInfo
	for i := range cf.symbols {
		if cf.symbols[i].Name == name {
			s := &cf.symbols[i]
			if best == nil {
				best = s
			} else if isDefinitionType(s.Type) && !isDefinitionType(best.Type) {
				best = s
			} else if isDefinitionType(s.Type) == isDefinitionType(best.Type) &&
				(s.EndLine-s.StartLine) > (best.EndLine-best.StartLine) {
				best = s
			}
		}
	}
	if best != nil {
		return best, nil
	}
	return nil, o.symbolNotFoundError(ctx, name, file)
}


// isDefinitionType returns true for symbol types that represent definitions
// (struct, class, enum, type, interface) vs implementations (impl, method, function).
func isDefinitionType(typ string) bool {
	switch typ {
	case "struct", "class", "enum", "type", "interface", "variable":
		return true
	}
	return false
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
		if Supported(path) {
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

	WalkDirFiles(o.root, absDir, func(path string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if Supported(path) {
			ch <- path
		}
		return nil
	})
	close(ch)
	wg.Wait()
	return results
}

func (o *OnDemand) ResolveSymbol(ctx context.Context, name string) (*SymbolInfo, error) {
	// Fast path: use symbol index if available.
	// With per-file dirty tracking, we use indexed results for clean files
	// and skip entries from dirty files (they'll be found by parseCandidateFiles).
	if entries := idx.LookupSymbols(o.edrDir, name); len(entries) > 0 {
		_, files := idx.LoadAllSymbols(o.edrDir)
		var candidates []SymbolInfo
		for _, e := range entries {
			rel := ""
			if int(e.FileID) < len(files) {
				rel = files[e.FileID].Path
			}
			// Skip entries from dirty files — they may be stale
			if idx.IsDirtyFile(o.edrDir, rel) {
				continue
			}
			absPath := filepath.Join(o.root, rel)
			// Validate byte offsets against actual file size to catch stale index entries.
			if fi, err := os.Stat(absPath); err != nil || int64(e.EndByte) > fi.Size() {
				continue
			}
			candidates = append(candidates, SymbolInfo{
				Name: e.Name, Type: e.Kind.String(),
				File:      absPath,
				StartLine: e.StartLine, EndLine: e.EndLine,
				StartByte: e.StartByte, EndByte: e.EndByte,
			})
		}
		if len(candidates) == 1 {
			return &candidates[0], nil
		}
		if len(candidates) > 0 {
			// Always go through ranking for multiple candidates.
			// preferDefinition is too aggressive for common names.
			return nil, &AmbiguousSymbolError{Name: name, Root: o.root, Candidates: candidates}
		}
		// All entries were from dirty files — fall through to parse
	}

	all := o.parseCandidateFiles(ctx, name)
	var candidates []SymbolInfo
	for _, cf := range all {
		for _, s := range cf.symbols {
			if strings.EqualFold(s.Name, name) {
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
	return nil, &AmbiguousSymbolError{Name: name, Root: o.root, Candidates: candidates}
}

func (o *OnDemand) SearchSymbols(ctx context.Context, pattern string, limit ...int) ([]SymbolInfo, error) {
	lim := 50
	if len(limit) > 0 && limit[0] > 0 {
		lim = limit[0]
	}

	// Fast path: scan symbol names from index
	if !idx.IsDirty(o.edrDir) && idx.HasSymbolIndex(o.edrDir) {
		allSyms, files := idx.LoadAllSymbols(o.edrDir)
		if allSyms != nil {
			lowerPattern := strings.ToLower(pattern)
			var results []SymbolInfo
			for _, s := range allSyms {
				if strings.Contains(strings.ToLower(s.Name), lowerPattern) {
					file := ""
					if int(s.FileID) < len(files) {
						file = filepath.Join(o.root, files[s.FileID].Path)
					}
					results = append(results, SymbolInfo{
						Name: s.Name, Type: s.Kind.String(), File: file,
						StartLine: s.StartLine, EndLine: s.EndLine,
						StartByte: s.StartByte, EndByte: s.EndByte,
					})
					if len(results) >= lim {
						return results, nil
					}
				}
			}
			return results, nil
		}
	}

	all := o.parseCandidateFiles(ctx, pattern)
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
	// Fast path: read from symbol index
	if !idx.IsDirty(o.edrDir) && idx.HasSymbolIndex(o.edrDir) {
		allSyms, files := idx.LoadAllSymbols(o.edrDir)
		if allSyms != nil {
			results := make([]SymbolInfo, 0, len(allSyms))
			for _, s := range allSyms {
				file := ""
				if int(s.FileID) < len(files) {
					file = filepath.Join(o.root, files[s.FileID].Path)
				}
				results = append(results, SymbolInfo{
					Name: s.Name, Type: s.Kind.String(), File: file,
					StartLine: s.StartLine, EndLine: s.EndLine,
					StartByte: s.StartByte, EndByte: s.EndByte,
				})
			}
			return results, nil
		}
	}

	all := o.parseAll(ctx)
	var results []SymbolInfo
	for _, cf := range all {
		results = append(results, cf.symbols...)
	}
	return results, nil
}

func (o *OnDemand) FilteredSymbols(ctx context.Context, dir, symbolType, namePattern string) ([]SymbolInfo, error) {
	// When dir is set, only parse that subtree instead of the whole repo.
	absDir := ""
	if dir != "" {
		if filepath.IsAbs(dir) {
			absDir = dir
		} else {
			absDir = filepath.Join(o.root, dir)
		}
	}

	// Fast path: use symbol index when available and no dir filter
	if absDir == "" && idx.HasSymbolIndex(o.edrDir) && !idx.IsDirty(o.edrDir) {
		return o.filteredSymbolsFromIndex(namePattern, symbolType)
	}

	var parsed map[string]*cachedFile
	if absDir != "" {
		parsed = o.parseDir(ctx, absDir)
	} else if namePattern != "" {
		parsed = o.parseCandidateFiles(ctx, namePattern)
	} else {
		parsed = o.parseAll(ctx)
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

// filteredSymbolsFromIndex reads symbols directly from the persistent index.
func (o *OnDemand) filteredSymbolsFromIndex(namePattern, symbolType string) ([]SymbolInfo, error) {
	allSyms, files := idx.LoadAllSymbols(o.edrDir)
	if allSyms == nil {
		return nil, nil
	}

	lowerPattern := strings.ToLower(namePattern)
	var results []SymbolInfo
	for _, s := range allSyms {
		if symbolType != "" && s.Kind.String() != symbolType {
			continue
		}
		if namePattern != "" && !strings.Contains(strings.ToLower(s.Name), lowerPattern) {
			continue
		}
		file := ""
		if int(s.FileID) < len(files) {
			file = filepath.Join(o.root, files[s.FileID].Path)
		}
		results = append(results, SymbolInfo{
			Name: s.Name, Type: s.Kind.String(), File: file,
			StartLine: s.StartLine, EndLine: s.EndLine,
			StartByte: s.StartByte, EndByte: s.EndByte,
		})
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
			// Skip the target symbol itself — its body contains its own name.
			if s.File == symbolFile && s.Name == symbolName {
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
	o.mu.RLock()
	cached := o.fileCount
	var symCount int
	for _, cf := range o.cache {
		symCount += len(cf.symbols)
	}
	o.mu.RUnlock()

	if cached >= 0 {
		return cached, symCount, nil
	}

	var fileCount int
	WalkRepoFiles(o.root, func(path string) error {
		if Supported(path) {
			fileCount++
		}
		return nil
	})
	o.mu.Lock()
	o.fileCount = fileCount
	o.mu.Unlock()
	return fileCount, symCount, nil
}

// parseCandidateFiles uses the trigram index to find files likely containing
// the given text, then parses only those files. Falls back to parseAll when
// no index is available.
func (o *OnDemand) parseCandidateFiles(ctx context.Context, text string) map[string]*cachedFile {
	tris := idx.QueryTrigrams(strings.ToLower(text))
	indexed := idx.IndexedPaths(o.edrDir)

	var candidatePaths []string
	if indexed != nil && len(tris) > 0 {
		if paths, ok := idx.Query(o.edrDir, tris); ok {
			for _, rel := range paths {
				abs := filepath.Join(o.root, rel)
				if Supported(abs) {
					candidatePaths = append(candidatePaths, abs)
				}
			}
		}
	}

	if indexed == nil {
		return o.parseAll(ctx)
	}

	// For short text (< 3 chars), pad with a space prefix to produce at
	// least one trigram. Symbol definitions are almost always preceded by
	// whitespace ("struct rq", "int rq", "def rq"), so " <name>" narrows
	// the candidate set dramatically without missing definitions.
	if len(tris) == 0 && len(text) >= 2 {
		tris = idx.QueryTrigrams(" " + strings.ToLower(text))
		if len(tris) > 0 {
			if paths, ok := idx.Query(o.edrDir, tris); ok {
				for _, rel := range paths {
					abs := filepath.Join(o.root, rel)
					if Supported(abs) {
						candidatePaths = append(candidatePaths, abs)
					}
				}
			}
		}
	}

	// If still no trigrams (1-char names or no index matches), full parse.
	if len(tris) == 0 {
		return o.parseAll(ctx)
	}

	textLower := []byte(strings.ToLower(text))

	// Build per-file dirty set for targeted re-checking.
	dirtySet := make(map[string]bool)
	for _, f := range idx.DirtyFiles(o.edrDir) {
		dirtySet[f] = true
	}

	// Pre-filter trigram candidates by content (trigrams are approximate).
	var filteredPaths []string
	for _, p := range candidatePaths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if bytes.Contains(bytes.ToLower(data), textLower) {
			filteredPaths = append(filteredPaths, p)
		}
	}

	// Collect walk candidates: paths that need content-check + parse.
	// With trigrams: only dirty + unindexed files (trigram results cover the rest).
	// Without trigrams (short names): all files (can't narrow by trigrams).
	trigramHandled := make(map[string]bool, len(candidatePaths))
	for _, p := range candidatePaths {
		if rel, err := filepath.Rel(o.root, p); err == nil {
			trigramHandled[rel] = true
		}
	}

	// Only parse the specific dirty files — no full walk needed.
	// The trigram index covers all indexed clean files.
	var walkPaths []string
	for rel := range dirtySet {
		if trigramHandled[rel] {
			continue
		}
		abs := filepath.Join(o.root, rel)
		if Supported(abs) {
			walkPaths = append(walkPaths, abs)
		}
	}

	// Parse all candidates in parallel. Workers do content-check for
	// walk candidates (avoids double-reading since parseFile reads too).
	results := make(map[string]*cachedFile, len(filteredPaths))
	var mu sync.Mutex
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}

	type parseJob struct {
		path         string
		contentCheck bool // true = read+check content before parsing
	}
	ch := make(chan parseJob, workers*4)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range ch {
				if ctx.Err() != nil {
					return
				}
				if job.contentCheck {
					data, err := os.ReadFile(job.path)
					if err != nil {
						continue
					}
					if !bytes.Contains(bytes.ToLower(data), textLower) {
						continue
					}
				}
				cf, err := o.parseFile(job.path)
				if err != nil || cf == nil {
					continue
				}
				mu.Lock()
				results[job.path] = cf
				mu.Unlock()
			}
		}()
	}

	// Feed trigram-confirmed candidates (no content check needed)
	for _, p := range filteredPaths {
		ch <- parseJob{path: p}
	}
	// Feed walk candidates (need content check in worker)
	for _, p := range walkPaths {
		ch <- parseJob{path: p, contentCheck: true}
	}

	close(ch)
	wg.Wait()
	return results
}

// FileCountExceeds returns true if the repo has more than n parseable files.
// Stops walking as soon as the threshold is exceeded, avoiding a full walk.
func (o *OnDemand) FileCountExceeds(n int) bool {
	count := 0
	WalkRepoFiles(o.root, func(path string) error {
		if Supported(path) {
			count++
			if count > n {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return count > n
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
