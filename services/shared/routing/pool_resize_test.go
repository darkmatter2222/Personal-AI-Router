// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"sync"
	"testing"
	"time"

	"nvpair-shared/noderec"
)

// Pool.Resize tests: capacity reconfiguration semantics (§30).

func TestPoolResize_Expansion(t *testing.T) {
	p := NewPool(1)
	if !p.Reserve() {
		t.Fatal("first reservation should succeed")
	}
	if p.Reserve() {
		t.Fatal("second reservation should fail at capacity 1")
	}
	p.Resize(4)
	// After expansion to 4, three more reservations succeed (total 4).
	if !p.Reserve() {
		t.Fatal("third reservation should succeed after expansion to 4")
	}
	if !p.Reserve() {
		t.Fatal("fourth reservation should succeed after expansion to 4")
	}
	if !p.Reserve() {
		t.Fatal("fifth reservation should succeed after expansion to 4")
	}
	if p.Reserve() {
		t.Fatal("sixth reservation should fail at capacity 4")
	}
	if p.Active() != 4 {
		t.Fatalf("active = %d, want 4", p.Active())
	}
}

func TestPoolResize_Contraction(t *testing.T) {
	p := NewPool(4)
	for i := 0; i < 4; i++ {
		if !p.Reserve() {
			t.Fatalf("reservation %d should succeed", i)
		}
	}
	p.Resize(1)
	// Active count (4) exceeds new capacity (1): existing reservations remain
	// valid; new ones fail until active drops below the new cap.
	if p.Reserve() {
		t.Fatal("new reservation should fail when active > new capacity")
	}
	// Release until active < new capacity.
	p.Release()
	p.Release()
	p.Release()
	// Now active = 1, which equals the new capacity. No new reservation.
	if p.Reserve() {
		t.Fatal("new reservation should fail at active == capacity")
	}
	// Release one more: active = 0 < 1.
	p.Release()
	if !p.Reserve() {
		t.Fatal("new reservation should succeed when active < capacity")
	}
}

func TestPoolResize_ContractionBelowZero(t *testing.T) {
	p := NewPool(4)
	p.Resize(-1)
	// -1 clamps to 0 = unbounded.
	if !p.Reserve() {
		t.Fatal("reservation should succeed with unbounded pool")
	}
	if !p.Reserve() {
		t.Fatal("reservation should succeed with unbounded pool")
	}
}

func TestPoolResize_ZeroIsUnbounded(t *testing.T) {
	p := NewPool(0)
	// Zero capacity = unbounded (legacy default).
	for i := 0; i < 100; i++ {
		if !p.Reserve() {
			t.Fatalf("reservation %d should succeed with unbounded pool", i)
		}
	}
	if p.Active() != 100 {
		t.Fatalf("active = %d, want 100", p.Active())
	}
}

func TestPoolResize_ConcurrencySafe(t *testing.T) {
	p := NewPool(2)
	var wg sync.WaitGroup
	const n = 20
	// Half the goroutines reserve, half resize.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				p.Reserve()
			} else {
				p.Resize(i % 5)
			}
		}(i)
	}
	wg.Wait()
	// No panic, no negative active.
	if p.Active() < 0 {
		t.Fatalf("active = %d, want >= 0", p.Active())
	}
}

func TestPoolRelease_NoNegative(t *testing.T) {
	p := NewPool(1)
	// Release without any reservation: should not go negative.
	p.Release()
	p.Release()
	p.Release()
	if p.Active() != 0 {
		t.Fatalf("active = %d, want 0 (no negative)", p.Active())
	}
	// Still reservable.
	if !p.Reserve() {
		t.Fatal("reservation should succeed after extra releases")
	}
}

// Failover race test (§29): multiple requests race to reserve a single slot
// on the failover target. Only one may succeed.

func TestFailoverRace_SingleSlot(t *testing.T) {
	// A is full, B has exactly 1 slot. 10 requests race to failover to B.
	poolB := NewPool(1)
	const n = 10
	results := make(chan bool, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- poolB.Reserve()
		}()
	}
	wg.Wait()
	close(results)
	admitted := 0
	for ok := range results {
		if ok {
			admitted++
		}
	}
	if admitted != 1 {
		t.Fatalf("admitted %d to B, want exactly 1", admitted)
	}
}

func TestFailoverRace_MultipleSlots(t *testing.T) {
	// B has 3 slots. 10 requests race. Exactly 3 may be admitted.
	poolB := NewPool(3)
	const n = 10
	results := make(chan bool, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- poolB.Reserve()
		}()
	}
	wg.Wait()
	close(results)
	admitted := 0
	for ok := range results {
		if ok {
			admitted++
		}
	}
	if admitted != 3 {
		t.Fatalf("admitted %d to B, want exactly 3", admitted)
	}
}

// Select with concurrent reservation: multiple Select calls racing on the
// same candidate set. Each must get a distinct slot or be rejected.

func TestSelectConcurrent_CapacityEnforced(t *testing.T) {
	pool := NewPool(2)
	cands := []Candidate{
		{ID: "a", Healthy: true, Enabled: true, Capacity: pool,
			Caps: EndpointCaps{Text: boolPtr(true)}},
	}
	req := Req{Text: true}
	results := make(chan string, 10)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := Select(cands, req)
			results <- out.SelectedID
		}()
	}
	wg.Wait()
	close(results)
	selected := 0
	for id := range results {
		if id == "a" {
			selected++
		}
	}
	if selected != 2 {
		t.Fatalf("selected %d, want exactly 2 (capacity 2)", selected)
	}
}

// Timeout defaults tests (§82).

func TestResolveTimeouts_Nil(t *testing.T) {
	p := ResolveTimeouts(nil)
	if p.Connect != DefaultConnectTimeout {
		t.Errorf("connect = %v, want %v", p.Connect, DefaultConnectTimeout)
	}
	if p.ResponseHeader != DefaultResponseHeaderTimeout {
		t.Errorf("response header = %v, want %v", p.ResponseHeader, DefaultResponseHeaderTimeout)
	}
	if p.FirstByte != DefaultFirstByteTimeout {
		t.Errorf("first byte = %v, want %v", p.FirstByte, DefaultFirstByteTimeout)
	}
}

func TestResolveTimeouts_AllZero(t *testing.T) {
	tms := &noderec.EngineTimeouts{ConnectMS: 0, ResponseHeaderMS: 0, FirstByteMS: 0}
	p := ResolveTimeouts(tms)
	if p.Connect != DefaultConnectTimeout || p.ResponseHeader != DefaultResponseHeaderTimeout || p.FirstByte != DefaultFirstByteTimeout {
		t.Fatalf("zero fields should fall back to defaults: %+v", p)
	}
}

func TestResolveTimeouts_PartialOverride(t *testing.T) {
	tms := &noderec.EngineTimeouts{ConnectMS: 250, ResponseHeaderMS: 0, FirstByteMS: 30000}
	p := ResolveTimeouts(tms)
	if p.Connect != 250e6 { // 250ms in nanoseconds
		t.Errorf("connect = %v, want 250ms", p.Connect)
	}
	if p.ResponseHeader != DefaultResponseHeaderTimeout {
		t.Errorf("response header should be default: %v", p.ResponseHeader)
	}
	if p.FirstByte != 30e9 { // 30000ms in nanoseconds
		t.Errorf("first byte = %v, want 30000ms", p.FirstByte)
	}
}

func TestResolveTimeouts_NegativeValues(t *testing.T) {
	tms := &noderec.EngineTimeouts{ConnectMS: -1, ResponseHeaderMS: -5, FirstByteMS: -100}
	p := ResolveTimeouts(tms)
	if p.Connect != DefaultConnectTimeout || p.ResponseHeader != DefaultResponseHeaderTimeout || p.FirstByte != DefaultFirstByteTimeout {
		t.Fatalf("negative fields should fall back to defaults: %+v", p)
	}
}

func TestResolveTimeouts_IndividualFields(t *testing.T) {
	cases := []struct {
		name     string
		tms      *noderec.EngineTimeouts
		wantConn time.Duration
		wantResp time.Duration
		wantFirst time.Duration
	}{
		{"only connect", &noderec.EngineTimeouts{ConnectMS: 100}, 100e6, DefaultResponseHeaderTimeout, DefaultFirstByteTimeout},
		{"only response header", &noderec.EngineTimeouts{ResponseHeaderMS: 5000}, DefaultConnectTimeout, 5e9, DefaultFirstByteTimeout},
		{"only first byte", &noderec.EngineTimeouts{FirstByteMS: 60000}, DefaultConnectTimeout, DefaultResponseHeaderTimeout, 60e9},
		{"all custom", &noderec.EngineTimeouts{ConnectMS: 100, ResponseHeaderMS: 5000, FirstByteMS: 60000}, 100e6, 5e9, 60e9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := ResolveTimeouts(tc.tms)
			if p.Connect != tc.wantConn {
				t.Errorf("connect = %v, want %v", p.Connect, tc.wantConn)
			}
			if p.ResponseHeader != tc.wantResp {
				t.Errorf("response header = %v, want %v", p.ResponseHeader, tc.wantResp)
			}
			if p.FirstByte != tc.wantFirst {
				t.Errorf("first byte = %v, want %v", p.FirstByte, tc.wantFirst)
			}
		})
	}
}
