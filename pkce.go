package main

import (
	"sync"
	"time"
)

type pkceEntry struct {
	verifier  string
	createdAt time.Time
}

type pkceStore struct {
	mu    sync.Mutex
	store map[string]*pkceEntry
	ttl   time.Duration
}

func newPKCEStore() *pkceStore {
	return &pkceStore{
		store: make(map[string]*pkceEntry),
		ttl:   10 * time.Minute,
	}
}

func (p *pkceStore) Set(state, verifier string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.store[state] = &pkceEntry{verifier: verifier, createdAt: time.Now()}
	for k, v := range p.store {
		if time.Since(v.createdAt) > p.ttl {
			delete(p.store, k)
		}
	}
}

func (p *pkceStore) PopVerifier(state string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.store[state]
	if !ok {
		return "", false
	}
	delete(p.store, state)
	if time.Since(e.createdAt) > p.ttl {
		return "", false
	}
	return e.verifier, true
}
