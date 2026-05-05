package main

import (
	"sync/atomic"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

// queryCache is a small in-process LRU with TTL, sized for analyst workloads
// where a few hundred unique query shapes are revisited frequently.
//
// We intentionally use bytes (already-encoded JSON) as the value, not a Go
// struct, so that a cache hit costs only one memcpy + one Write — no
// re-marshalling per request.
//
// The 'namespace' atomic is used for whole-cache invalidation: bumping it
// causes every existing key to miss because we mix the namespace into every
// key at lookup time. Cheaper than walking the LRU to delete.
type queryCache struct {
	store     *expirable.LRU[string, []byte]
	namespace atomic.Uint64
}

func newQueryCache(maxEntries int, ttl time.Duration) *queryCache {
	if maxEntries <= 0 {
		maxEntries = 512
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	c := &queryCache{
		store: expirable.NewLRU[string, []byte](maxEntries, nil, ttl),
	}
	c.namespace.Store(1)
	return c
}

func (c *queryCache) versioned(key string) string {
	// Mix the current namespace into the lookup key. After a Bump(), all
	// previously stored entries are unreachable.
	return key + "#v" + uint64ToString(c.namespace.Load())
}

func (c *queryCache) Get(key string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.store.Get(c.versioned(key))
	if !ok {
		return nil, false
	}
	// Return a copy so the caller can safely write into it without mutating
	// the cached entry. This is cheap; the JSON payloads are small.
	out := make([]byte, len(v))
	copy(out, v)
	return out, true
}

func (c *queryCache) Set(key string, val []byte) {
	if c == nil {
		return
	}
	cp := make([]byte, len(val))
	copy(cp, val)
	c.store.Add(c.versioned(key), cp)
}

// Bump invalidates every entry. Called after admin writes (POST/DELETE) and
// after CSV imports.
func (c *queryCache) Bump() {
	if c == nil {
		return
	}
	c.namespace.Add(1)
}

func uint64ToString(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
