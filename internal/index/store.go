package index

import "context"

// SymbolStore is the interface for symbol lookup and cross-file queries.
// Implementation: *OnDemand (parse-on-demand).
type SymbolStore interface {
	// Path accessors
	Root() string
	EdrDir() string
	ResolvePath(path string) (string, error)
	ResolvePathReadOnly(path string) (string, error)

	// Single-file symbol lookup
	GetSymbol(ctx context.Context, file, name string) (*SymbolInfo, error)
	GetSymbolsByFile(ctx context.Context, file string) ([]SymbolInfo, error)
	GetContainerAt(ctx context.Context, file string, line int) (*SymbolInfo, error)

	// Cross-file symbol search
	ResolveSymbol(ctx context.Context, name string) (*SymbolInfo, error)
	SearchSymbols(ctx context.Context, pattern string, limit ...int) ([]SymbolInfo, error)
	AllSymbols(ctx context.Context) ([]SymbolInfo, error)
	FilteredSymbols(ctx context.Context, dir, symbolType, namePattern string) ([]SymbolInfo, error)

	// Cross-file references (heuristic: body-text substring matching, not semantic)
	FindSemanticCallers(ctx context.Context, symbolName, symbolFile string) ([]SymbolInfo, error)
	FindSameFileCallers(ctx context.Context, symbolName, symbolFile string) ([]SymbolInfo, error)
	FindSemanticReferences(ctx context.Context, symbolName, symbolFile string) ([]SymbolInfo, error)

	// File metadata
	GetFileHash(ctx context.Context, path string) (string, error)
	Stats(ctx context.Context) (files int, symbols int, err error)
	IndexWarnings() []FileError

	// Mutation (post-edit cache invalidation)
	InvalidateFiles(ctx context.Context, paths []string) error
	WithWriteLock(fn func() error) error

	// Lifecycle
	Close() error
}

// Compile-time check
var _ SymbolStore = (*OnDemand)(nil)
