package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// cacheEntry holds a cached tool result with expiry.
type cacheEntry struct {
	result  string
	expires time.Time
}

// CacheMiddleware caches results of read-only tools to avoid redundant
// calls within a conversation turn. Only tools in the cacheable set are cached.
type CacheMiddleware struct {
	mu        sync.Mutex
	entries   map[string]cacheEntry
	ttl       time.Duration
	cacheable map[string]bool
}

// NewCacheMiddleware creates a caching middleware.
// cacheable is the set of tool names whose results can be cached.
func NewCacheMiddleware(ttl time.Duration, cacheable []string) *CacheMiddleware {
	m := make(map[string]bool, len(cacheable))
	for _, name := range cacheable {
		m[name] = true
	}
	return &CacheMiddleware{
		entries:   make(map[string]cacheEntry),
		ttl:       ttl,
		cacheable: m,
	}
}

// Middleware returns a Registry Middleware function.
func (c *CacheMiddleware) Middleware() Middleware {
	return func(ctx context.Context, toolName string, args string, next func(context.Context, string) (string, error)) (string, error) {
		if !c.cacheable[toolName] {
			return next(ctx, args)
		}

		key := cacheKey(toolName, args)

		c.mu.Lock()
		if entry, ok := c.entries[key]; ok && time.Now().Before(entry.expires) {
			c.mu.Unlock()
			return entry.result, nil
		}
		c.mu.Unlock()

		result, err := next(ctx, args)
		if err != nil {
			return result, err
		}

		c.mu.Lock()
		c.entries[key] = cacheEntry{
			result:  result,
			expires: time.Now().Add(c.ttl),
		}
		c.mu.Unlock()

		return result, nil
	}
}

// Invalidate removes all cached entries. Call on file writes or other mutations.
func (c *CacheMiddleware) Invalidate() {
	c.mu.Lock()
	c.entries = make(map[string]cacheEntry)
	c.mu.Unlock()
}

func cacheKey(toolName, args string) string {
	h := sha256.Sum256([]byte(toolName + "\x00" + args))
	return hex.EncodeToString(h[:16])
}
