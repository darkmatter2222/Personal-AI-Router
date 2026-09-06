// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"fmt"
	"sync"
	"testing"
)

// TestPool_ReleaseOrderIndependence: releasing the middle reservation frees a
// slot regardless of acquisition order.
func TestPool_ReleaseOrderIndependence(t *testing.T) {
	p := NewPool(3)
	r1, _ := p.Reserve()
	r2, _ := p.Reserve()
	r3, _ := p.Reserve()
	if _, ok := p.Reserve(); ok {
		t.Fatal("cap 3 should be full")
	}
	r2()
	if p.Used() != 2 {
		t.Fatalf("used = %d, want 2", p.Used())
	}
	if _, ok := p.Reserve(); !ok {
		t.Fatal("middle release should free a slot")
	}
	r1()
	r3()
}

// TestPools_ReconcileSameCapsPreservesInflight: reconciling to identical caps is
// a no-op that never disturbs in-flight reservations.
func TestPools_ReconcileSameCapsPreservesInflight(t *testing.T) {
	ps := NewPools()
	r, _ := ps.Reserve("A", 3)
	ps.Reconcile(map[string]int{"A": 3})
	if u, _ := ps.Used("A"); u != 1 {
		t.Fatalf("in-flight lost on same-caps reconcile: %d", u)
	}
	if c, _ := ps.Cap("A"); c != 3 {
		t.Fatalf("cap changed on same-caps reconcile: %d", c)
	}
	r()
}

// TestPools_ReconcileToUnbounded: reconfiguring a full bounded pool to unbounded
// (0) immediately admits again.
func TestPools_ReconcileToUnbounded(t *testing.T) {
	ps := NewPools()
	r1, _ := ps.Reserve("A", 1)
	if _, ok := ps.Reserve("A", 1); ok {
		t.Fatal("cap 1 should be full")
	}
	ps.Reconcile(map[string]int{"A": 0})
	r2, ok := ps.Reserve("A", 1)
	if !ok {
		t.Fatal("unbounded pool should admit")
	}
	r1()
	r2()
}

func TestPool_AvailableAtBoundaries(t *testing.T) {
	p := NewPool(1)
	if !p.Available() {
		t.Fatal("empty cap-1 must be available")
	}
	r, _ := p.Reserve()
	if p.Available() {
		t.Fatal("full cap-1 must not be available")
	}
	r()
	if !p.Available() {
		t.Fatal("released cap-1 must be available")
	}
	if !NewPool(0).Available() {
		t.Fatal("unbounded must always be available")
	}
}

// TestPools_MultipleIDsIndependent: concurrent reservations against distinct
// endpoint ids don't contend; each keeps its own capacity.
func TestPools_MultipleIDsIndependent(t *testing.T) {
	ps := NewPools()
	const ids = 8
	var wg sync.WaitGroup
	for i := 0; i < ids; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("e%d", i)
			r, ok := ps.Reserve(id, 1)
			if !ok {
				t.Errorf("id %s first reserve failed", id)
				return
			}
			if _, ok2 := ps.Reserve(id, 1); ok2 {
				t.Errorf("id %s cap-1 should be full", id)
			}
			r()
		}(i)
	}
	wg.Wait()
	if ps.Len() != ids {
		t.Fatalf("want %d independent pools, got %d", ids, ps.Len())
	}
}

// TestPools_ReconcileRaceWithRelease: a race-detector target that hammers
// Reconcile against a churn of reserve/release; no lost release, no negative.
func TestPools_ReconcileRaceWithRelease(t *testing.T) {
	ps := NewPools()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				ps.Reconcile(map[string]int{"A": 4})
			}
		}
	}()
	done := make(chan struct{})
	go func() {
		for n := 0; n < 3000; n++ {
			if r, ok := ps.Reserve("A", 4); ok {
				r()
			}
		}
		close(done)
	}()
	<-done
	close(stop)
	wg.Wait()
	if u, ok := ps.Used("A"); ok && u != 0 {
		t.Fatalf("in-flight leaked after churn: %d", u)
	}
}
