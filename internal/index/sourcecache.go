package index

import (
	"context"
	"os"
	"sync"
)

// sourceCache provides request-scoped file content caching to avoid
// repeated os.ReadFile calls across search, gather, and signature extraction.
type sourceCache struct {
	mu    sync.RWMutex
	files map[string][]byte
}

type sourceCacheKey struct{}

// WithSourceCache returns a context that carries a per-request source cache.
// All CachedReadFile calls using this context will share the cache.
// If the context already has a cache, it is returned unchanged.
func WithSourceCache(ctx context.Context) context.Context {
	if _, ok := ctx.Value(sourceCacheKey{}).(*sourceCache); ok {
		return ctx
	}
	return context.WithValue(ctx, sourceCacheKey{}, &sourceCache{files: make(map[string][]byte)})
}

// CachedReadFile reads a file, using the context's source cache if available.
// Falls back to os.ReadFile if no cache is in the context.
func CachedReadFile(ctx context.Context, path string) ([]byte, error) {
	sc, ok := ctx.Value(sourceCacheKey{}).(*sourceCache)
	if !ok {
		return os.ReadFile(path)
	}
	sc.mu.RLock()
	if data, found := sc.files[path]; found {
		sc.mu.RUnlock()
		return data, nil
	}
	sc.mu.RUnlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sc.mu.Lock()
	sc.files[path] = data
	sc.mu.Unlock()
	return data, nil
}
