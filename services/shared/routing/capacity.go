// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import "sync"

// Pool is a concurrency-safe admission gate for one endpoint. It bounds the
// number of concurrently admitted requests. A capacity of 0 (or negative) means
// unbounded — the legacy default where no admission gate is applied — while
// still tracking in-flight count for observability.
//
// A reservation is authoritative: Reserve returns a release function only when
// admission succeeded, and that release is idempotent (safe to call any number
// of times, from any terminal path) so capacity is released exactly once per
// successful reservation, never double-released, and never driven negative.
type Pool struct {
	mu   sync.Mutex
	cap  int
	used int
}

// NewPool creates a pool with the given capacity (0 = unbounded).
func NewPool(capacity int) *Pool {
	return &Pool{cap: capacity}
}

// Reserve attempts to admit one request. It returns a release function and true
// on success, or nil and false when the pool is full. The returned release is
// idempotent: extra calls are no-ops. Callers MUST honour the boolean — a
// failed reservation is authoritative and the request must NOT be forwarded.
func (p *Pool) Reserve() (release func(), ok bool) {
	p.mu.Lock()
	if p.cap > 0 && p.used >= p.cap {
		p.mu.Unlock()
		return nil, false
	}
	p.used++
	p.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			if p.used > 0 {
				p.used--
			}
			p.mu.Unlock()
		})
	}, true
}

// Resize changes the pool's capacity. In-flight reservations are never revoked:
// if the new capacity is below the current in-flight count, no new reservation
// succeeds until enough in-flight requests release, but the counter is never
// forced negative and no active request is disturbed.
func (p *Pool) Resize(capacity int) {
	p.mu.Lock()
	p.cap = capacity
	p.mu.Unlock()
}

// Used returns the current in-flight count.
func (p *Pool) Used() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.used
}

// Cap returns the configured capacity (0 = unbounded).
func (p *Pool) Cap() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cap
}

// Available reports whether a reservation would currently succeed. It is a
// point-in-time hint only (the answer can change the instant the lock is
// released); admission decisions must use Reserve's return value, not this.
func (p *Pool) Available() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cap <= 0 || p.used < p.cap
}

// Pools is a concurrency-safe registry of per-endpoint admission pools, keyed by
// stable endpoint id. It is the single place the executor reserves capacity and
// the control plane reconfigures it, so capacity policy stays consistent across
// concurrent requests and metadata refreshes.
type Pools struct {
	mu    sync.Mutex
	pools map[string]*Pool
}

// NewPools creates an empty pool registry.
func NewPools() *Pools {
	return &Pools{pools: make(map[string]*Pool)}
}

// Reserve admits one request against the endpoint's pool, creating the pool with
// the given capacity if it does not yet exist. It returns an idempotent release
// function and true on success, or nil and false when full.
//
// An EXISTING pool's capacity is NOT changed here: concurrent requests may carry
// slightly different capacity snapshots, and letting each Reserve rewrite the
// capacity would make it flap. Capacity changes flow exclusively through
// Reconcile (the metadata-refresh path), which has a single authoritative view.
func (ps *Pools) Reserve(id string, capacity int) (release func(), ok bool) {
	ps.mu.Lock()
	p := ps.pools[id]
	if p == nil {
		p = NewPool(capacity)
		ps.pools[id] = p
	}
	ps.mu.Unlock()
	// Reserve on the specific pool instance. If Reconcile later removes/recreates
	// this id, the captured instance still receives this reservation's release,
	// so the count can never leak into a different (recreated) pool.
	return p.Reserve()
}

// Reconcile makes the registry match the given capacity map exactly: every id in
// caps gets a pool (created if absent, resized if its capacity changed), and
// every pool whose id is NOT in caps is removed. Removing a pool with in-flight
// reservations is safe — the orphaned instance keeps receiving its own releases
// and is garbage-collected once they complete; a subsequently re-created id
// starts from a fresh, zeroed pool. This is what keeps a cached admission pool
// from remaining permanently stale when routing metadata changes.
func (ps *Pools) Reconcile(caps map[string]int) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for id, c := range caps {
		if p := ps.pools[id]; p != nil {
			p.Resize(c)
		} else {
			ps.pools[id] = NewPool(c)
		}
	}
	for id := range ps.pools {
		if _, keep := caps[id]; !keep {
			delete(ps.pools, id)
		}
	}
}

// Used returns the in-flight count for an endpoint and whether a pool exists.
func (ps *Pools) Used(id string) (int, bool) {
	ps.mu.Lock()
	p := ps.pools[id]
	ps.mu.Unlock()
	if p == nil {
		return 0, false
	}
	return p.Used(), true
}

// Cap returns the configured capacity for an endpoint and whether a pool exists.
func (ps *Pools) Cap(id string) (int, bool) {
	ps.mu.Lock()
	p := ps.pools[id]
	ps.mu.Unlock()
	if p == nil {
		return 0, false
	}
	return p.Cap(), true
}

// Len returns the number of registered pools.
func (ps *Pools) Len() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return len(ps.pools)
}
