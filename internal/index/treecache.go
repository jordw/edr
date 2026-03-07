package index

import (
	"hash/fnv"
	"sync"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

const treeCacheMaxSize = 32

// treeEntry is a cached tree-sitter parse tree.
type treeEntry struct {
	tree *tree_sitter.Tree
	key  uint64
	prev *treeEntry
	next *treeEntry
}

// treeCache is a concurrency-safe LRU cache for parsed tree-sitter trees.
// It avoids re-parsing the same file content across repeated reads at
// different depth levels, signature extraction, and reference searches.
type treeCache struct {
	mu      sync.Mutex
	entries map[uint64]*treeEntry
	head    *treeEntry // most recently used
	tail    *treeEntry // least recently used
	size    int
}

var globalTreeCache = &treeCache{
	entries: make(map[uint64]*treeEntry, treeCacheMaxSize),
}

// cacheKey computes a fast fingerprint from language + source content.
func cacheKey(langID string, src []byte) uint64 {
	h := fnv.New64a()
	h.Write([]byte(langID))
	h.Write([]byte{0})
	h.Write(src)
	return h.Sum64()
}

// get returns a cached tree or nil.
func (c *treeCache) get(key uint64) *tree_sitter.Tree {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil
	}
	c.moveToFront(e)
	return e.tree
}

// put stores a tree in the cache, evicting the LRU entry if full.
func (c *treeCache) put(key uint64, tree *tree_sitter.Tree) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Already cached (race with concurrent parse)
	if _, ok := c.entries[key]; ok {
		tree.Close()
		return
	}

	e := &treeEntry{tree: tree, key: key}
	c.entries[key] = e
	c.pushFront(e)
	c.size++

	for c.size > treeCacheMaxSize {
		c.evict()
	}
}

// clear evicts all entries and frees trees.
func (c *treeCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.entries {
		e.tree.Close()
	}
	c.entries = make(map[uint64]*treeEntry, treeCacheMaxSize)
	c.head = nil
	c.tail = nil
	c.size = 0
}

func (c *treeCache) moveToFront(e *treeEntry) {
	if c.head == e {
		return
	}
	c.remove(e)
	c.pushFront(e)
}

func (c *treeCache) pushFront(e *treeEntry) {
	e.prev = nil
	e.next = c.head
	if c.head != nil {
		c.head.prev = e
	}
	c.head = e
	if c.tail == nil {
		c.tail = e
	}
}

func (c *treeCache) remove(e *treeEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		c.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		c.tail = e.prev
	}
	e.prev = nil
	e.next = nil
}

func (c *treeCache) evict() {
	if c.tail == nil {
		return
	}
	e := c.tail
	c.remove(e)
	delete(c.entries, e.key)
	e.tree.Close()
	c.size--
}

// ClearTreeCache clears the global tree cache (e.g., on `init`).
func ClearTreeCache() {
	globalTreeCache.clear()
}

// cachedParseWith is like parseWith but checks the tree cache first.
// Cache hits avoid re-parsing (the expensive part of --depth and --signatures).
func cachedParseWith(lang *LangConfig, src []byte, fn func(root *tree_sitter.Node)) {
	key := cacheKey(lang.LangID, src)

	if tree := globalTreeCache.get(key); tree != nil {
		fn(tree.RootNode())
		return
	}

	parser := getParser(lang)
	tree := parser.Parse(src, nil)
	putParser(lang, parser)

	// Store in cache — cache owns the tree lifetime now.
	globalTreeCache.put(key, tree)

	// Re-fetch from cache in case put() found a duplicate and closed ours.
	if cached := globalTreeCache.get(key); cached != nil {
		fn(cached.RootNode())
	}
}
