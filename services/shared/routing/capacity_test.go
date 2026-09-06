// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestPool_ReserveUpToCapacity(t *testing.T) {
	p := NewPool(2)
	r1, ok1 := p.Reserve()
	r2, ok2 := p.Reserve()
	_, ok3 := p.Reserve()
	if !ok1 || !ok2 {
		t.Fatal("first two reservations must succeed")
	}
	if ok3 {
		t.Fatal("third reservation must fail at capacity 2")
	}
	if p.Used() != 2 {
		t.Fatalf("used = %d, want 2", p.Used())
	}
	// Release one; a new reservation now fits.
	r1()
	if p.Used() != 1 {
		t.Fatalf("used after release = %d, want 1", p.Used())
	}
	if _, ok := p.Reserve(); !ok {
		t.Fatal("reservation after release must succeed")
	}
	r2()
}

func TestPool_ReleaseIdempotent(t *testing.T) {
	p := NewPool(1)
	r, ok := p.Reserve()
	if !ok {
		t.Fatal("reserve failed")
	}
	r()
	r()
	r() // extra releases must be no-ops
	if p.Used() != 0 {
		t.Fatalf("used = %d after idempotent releases, want 0", p.Used())
	}
	// Counter must never go negative even with stray releases.
	if _, ok := p.Reserve(); !ok {
		t.Fatal("reserve after full release must succeed")
	}
}

func TestPool_Unbounded(t *testing.T) {
	p := NewPool(0)
	var releases []func()
	for i := 0; i < 1000; i++ {
		r, ok := p.Reserve()
		if !ok {
			t.Fatalf("unbounded pool refused reservation %d", i)
		}
		releases = append(releases, r)
	}
	if p.Used() != 1000 {
		t.Fatalf("unbounded used = %d, want 1000", p.Used())
	}
	if !p.Available() {
		t.Fatal("unbounded pool must always report available")
	}
	for _, r := range releases {
		r()
	}
	if p.Used() != 0 {
		t.Fatalf("used after releasing all = %d", p.Used())
	}
}

func TestPool_ResizeExpandContract(t *testing.T) {
	p := NewPool(1)
	r1, _ := p.Reserve()
	if _, ok := p.Reserve(); ok {
		t.Fatal("cap 1 should be full")
	}
	p.Resize(4) // expand
	r2, ok2 := p.Reserve()
	r3, ok3 := p.Reserve()
	if !ok2 || !ok3 {
		t.Fatal("expansion should admit more")
	}
	// Contract below active count (3 in flight -> cap 1).
	p.Resize(1)
	if _, ok := p.Reserve(); ok {
		t.Fatal("no new admission while in-flight exceeds new cap")
	}
	r1()
	r2()
	if _, ok := p.Reserve(); ok {
		t.Fatal("still over cap (1 in flight, cap 1)")
	}
	r3()
	if _, ok := p.Reserve(); !ok {
		t.Fatal("drained below cap; admission should resume")
	}
}

func TestPools_ReserveCreatesAndKeepsCap(t *testing.T) {
	ps := NewPools()
	r1, ok := ps.Reserve("A", 1)
	if !ok {
		t.Fatal("first reserve should succeed")
	}
	if _, ok := ps.Reserve("A", 5); ok {
		// Existing pool cap must NOT be widened by a later Reserve's snapshot.
		t.Fatal("existing pool cap must not change on Reserve; second should be full")
	}
	if c, _ := ps.Cap("A"); c != 1 {
		t.Fatalf("cap = %d, want unchanged 1", c)
	}
	r1()
}

func TestPools_Reconcile(t *testing.T) {
	ps := NewPools()
	r, _ := ps.Reserve("A", 4) // creates A cap 4, used 1
	// Expand A, add B, add C.
	ps.Reconcile(map[string]int{"A": 8, "B": 2, "C": 0})
	if c, _ := ps.Cap("A"); c != 8 {
		t.Fatalf("A cap after expand = %d, want 8", c)
	}
	if u, ok := ps.Used("A"); !ok || u != 1 {
		t.Fatalf("A used should survive reconcile: %d ok=%v", u, ok)
	}
	if _, ok := ps.Cap("B"); !ok {
		t.Fatal("B should exist after reconcile")
	}
	// Contract A below active is fine; removal drops D-like stale pools.
	ps.Reconcile(map[string]int{"A": 1}) // B and C removed
	if ps.Len() != 1 {
		t.Fatalf("only A should remain, len = %d", ps.Len())
	}
	if _, ok := ps.Cap("B"); ok {
		t.Fatal("B should have been removed as stale")
	}
	// A is at used 1, cap 1 now: full.
	if _, ok := ps.Reserve("A", 1); ok {
		t.Fatal("A should be full after contraction to 1 with 1 in flight")
	}
	// The in-flight release still applies to the surviving A pool.
	r()
	if u, _ := ps.Used("A"); u != 0 {
		t.Fatalf("A used after release = %d, want 0", u)
	}
}

func TestPools_RemovalThenRecreateIsFresh(t *testing.T) {
	ps := NewPools()
	r, _ := ps.Reserve("A", 2) // used 1
	ps.Reconcile(map[string]int{}) // remove A while in flight
	if ps.Len() != 0 {
		t.Fatalf("A should be removed, len=%d", ps.Len())
	}
	// Recreate A: it must start fresh (used 0), not inherit the orphaned count.
	r2, ok := ps.Reserve("A", 2)
	if !ok {
		t.Fatal("recreated A should admit")
	}
	if u, _ := ps.Used("A"); u != 1 {
		t.Fatalf("recreated A used = %d, want 1 (fresh)", u)
	}
	// The orphaned release must not affect the new pool.
	r()
	if u, _ := ps.Used("A"); u != 1 {
		t.Fatalf("orphaned release leaked into new pool: used = %d", u)
	}
	r2()
}

func TestPool_ConcurrentReserveExactCount(t *testing.T) {
	const cap = 8
	const goroutines = 200
	p := NewPool(cap)
	var success int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, ok := p.Reserve(); ok {
				atomic.AddInt64(&success, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if success != cap {
		t.Fatalf("exactly %d reservations should succeed, got %d", cap, success)
	}
	if p.Used() != cap {
		t.Fatalf("used = %d, want %d", p.Used(), cap)
	}
}

func TestPools_FailoverRaceSingleSlot(t *testing.T) {
	// Many requests race to fail over onto a single remaining slot on B; only
	// one may proceed.
	ps := NewPools()
	const goroutines = 100
	var success int64
	var releases sync.Map
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if r, ok := ps.Reserve("B", 1); ok {
				atomic.AddInt64(&success, 1)
				releases.Store(i, r)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if success != 1 {
		t.Fatalf("exactly one request may win the single slot, got %d", success)
	}
}

func TestPools_ConcurrentReserveResizeReconcile(t *testing.T) {
	// Race-detector target: hammer Reserve/Reconcile/Used concurrently. It must
	// not deadlock, panic, or drive the counter negative.
	ps := NewPools()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if r, ok := ps.Reserve("X", 4); ok {
					r()
				}
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				ps.Reconcile(map[string]int{"X": 2})
				ps.Reconcile(map[string]int{"X": 8, "Y": 1})
				_, _ = ps.Used("X")
			}
		}()
	}
	// Bounded work: each goroutine loops a fixed number of times via a counter
	// rather than time.
	done := make(chan struct{})
	go func() {
		var n int
		for n < 5000 {
			n++
			if r, ok := ps.Reserve("X", 4); ok {
				r()
			}
		}
		close(done)
	}()
	<-done
	close(stop)
	wg.Wait()
	if u, ok := ps.Used("X"); ok && u < 0 {
		t.Fatalf("counter went negative: %d", u)
	}
}
