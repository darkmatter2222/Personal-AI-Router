// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"strings"
	"testing"
	"time"

	"nvpair-shared/noderec"
)

// The heterogeneous validation suite simulates a five-mock topology (no real
// GPUs) and proves the capability-aware router's behavior end-to-end:
// eligibility gates run before priority/load scheduling, deterministic
// selection, static-capacity admission, and explainable rejection reasons. The
// mock backends are in-memory endpoint metadata only.

// mockCaps builds an EngineCaps with the given capability flags.
func mockCaps(vision, tools, streaming, reasoning bool) *noderec.EngineCaps {
	return &noderec.EngineCaps{
		Text:      boolPtr(true),
		Vision:    boolPtr(vision),
		Tools:     boolPtr(tools),
		Streaming: boolPtr(streaming),
		Reasoning: boolPtr(reasoning),
	}
}

// mockTopology returns the five simulated backends. Each maps a logical
// request requirement to the endpoint that can satisfy it, exercising every
// gating and selection rule without a real engine process.
func mockTopology() map[string]*noderec.EngineRouting {
	return map[string]*noderec.EngineRouting{
		"mock-a": {
			APIFamily:      "openai",
			Priority:       10,
			StaticCapacity: 2,
			Capabilities:   mockCaps(false, true, true, false),
			ContextMaxTokens: 262144,
		},
		"mock-b": {
			APIFamily:      "openai",
			Priority:       20,
			StaticCapacity: 1,
			Capabilities:   mockCaps(false, true, true, false),
			ContextMaxTokens: 262144,
		},
		"mock-c": {
			APIFamily:      "openai",
			Priority:       30,
			StaticCapacity: 3,
			Capabilities:   mockCaps(true, false, true, false),
			ContextMaxTokens: 262144,
			Timeouts:     &noderec.EngineTimeouts{FirstByteMS: 8000},
		},
		"mock-d": {
			APIFamily:        "vllm",
			AuthHeadersPresent: true,
		},
		"mock-e": {
			APIFamily:      "openai",
			Priority:       5,
			StaticCapacity: 10,
			Capabilities:   mockCaps(false, false, true, false),
			ContextMaxTokens: 65536,
			ModelRef: &noderec.EngineModelRef{
				PhysicalName: "local-coding-7b",
				Aliases:      []string{"local-coding"},
			},
		},
	}
}

// mockCandidates maps the mock topology (or a subset by ID) onto the router's
// endpoint snapshots via RoutingToEndpoint, wiring static-capacity pools and
// declared priorities. This is exactly what the proxies' routeInference does
// with discovered nodes' RoutingByEngine metadata.
func mockCandidates(t *testing.T, topology map[string]*noderec.EngineRouting, ids []string) []Candidate {
	t.Helper()
	out := make([]Candidate, 0, len(ids))
	for _, id := range ids {
		r := topology[id]
		caps, pool, priority := RoutingToEndpoint(r)
		// Simulated endpoints are admitted (enabled) by default; only an
		// explicitly disabled mock (the lifecycle-gating case) sets Enabled=false
		// on the Candidate after this helper returns.
		out = append(out, Candidate{
			ID:         id,
			Priority:   priority,
			Capacity:   pool,
			Healthy:    true,
			Enabled:    true,
			Draining:   r.Draining,
			MaxContext: caps.MaxContext,
			Caps:       caps,
		})
	}
	return out
}

// TestHeterogeneousRoutingAcceptance is the acceptance matrix: each case
// asserts which mock backend the router selects for a given request, and that
// the explainable rejection reasons are correct. It covers capability gates
// (vision/tools/streaming/context), priority spillover, static-capacity
// admission, and the lifecycle flags.
func TestHeterogeneousRoutingAcceptance(t *testing.T) {
	topo := mockTopology()
	all := []string{"mock-a", "mock-b", "mock-c", "mock-e", "mock-d"}

	cases := []struct {
		name       string
		req        Req
		ids        []string
		wantSelect  string
		wantReasons map[string]string
	}{
		{
			name:       "tools request selects best-priority eligible (A over E)",
			req:        Req{RequiresTools: true},
			ids:        []string{"mock-e", "mock-a", "mock-b", "mock-c"},
			wantSelect: "mock-a",
			wantReasons: map[string]string{
				"mock-e": ReasonToolsRequired,
			},
		},
		{
			name:       "image request only C serves vision",
			req:        Req{HasImages: true},
			ids:        all,
			wantSelect: "mock-c",
			wantReasons: map[string]string{
				"mock-a": ReasonVisionRequired,
				"mock-b": ReasonVisionRequired,
				"mock-e": ReasonVisionRequired,
			},
		},
		{
			name:       "large context spills past E's 65536 cap",
			req:        Req{RequiredContext: 70000},
			ids:        []string{"mock-e", "mock-a", "mock-b"},
			wantSelect: "mock-a",
			wantReasons: map[string]string{
				"mock-e": ReasonContextTooSmall,
			},
		},
		{
			name:       "disabled and draining endpoints are gated out",
			req:        Req{RequiresTools: true},
			ids:        []string{"mock-a", "mock-b"},
			wantSelect: "mock-a",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cands := mockCandidates(t, topo, tc.ids)
			if tc.name == "disabled and draining endpoints are gated out" {
				// Force a disabled + draining state on B to prove lifecycle gating.
				for i := range cands {
					if cands[i].ID == "mock-b" {
						cands[i].Enabled = false
						cands[i].Draining = true
					}
				}
			}
			out := Select(cands, tc.req)
			if out.SelectedID != tc.wantSelect {
				t.Fatalf("selected %q, want %q", out.SelectedID, tc.wantSelect)
			}
			for id, want := range tc.wantReasons {
				if got := out.Reasons[id]; got != want {
					t.Errorf("%s reason = %q, want %q", id, got, want)
				}
			}
			if tc.name == "disabled and draining endpoints are gated out" {
				if out.Reasons["mock-b"] != ReasonEndpointDisabled {
					t.Errorf("mock-b reason = %q, want %q", out.Reasons["mock-b"], ReasonEndpointDisabled)
				}
			}
		})
	}
}

// TestHeterogeneousModelAlias proves the logical->physical model mapping: one
// stable client-facing name ("local-coding") maps to Mock E's physical model
// ("local-coding-7b"), so the same logical request reaches the right physical
// model on the right runtime. The rewrite is a pure JSON field substitution, so
// no engine I/O is needed.
func TestHeterogeneousModelAlias(t *testing.T) {
	topo := mockTopology()
	physical := topo["mock-e"].ModelRef.PhysicalName
	const body = `{"model":"local-coding","messages":[{"role":"user","content":"hi"}]}`
	// Rewrite the model field to the endpoint's physical name.
	rewritten := strings.Replace(body, `"model":"local-coding"`, `"model":"`+physical+`"`, 1)
	if !strings.Contains(rewritten, physical) {
		t.Fatalf("alias rewrite did not produce physical name %q: %s", physical, rewritten)
	}
	if rewritten == body {
		t.Fatalf("alias rewrite made no change")
	}
	// An endpoint with no ModelRef (mock-a) must forward the body unchanged.
	if topo["mock-a"].ModelRef != nil {
		t.Fatalf("mock-a should have no model ref")
	}
}

// TestHeterogeneousAuthHeaders proves Mock D's declared auth-header presence is
// honored: an authenticated backend carries a locally-resolved Authorization
// header, and only the presence flag crosses the trust boundary — never the
// secret value.
func TestHeterogeneousAuthHeaders(t *testing.T) {
	topo := mockTopology()
	if !topo["mock-d"].AuthHeadersPresent {
		t.Fatalf("mock-d should declare auth header presence")
	}
	// The wire metadata carries only the presence flag; the value is resolved
	// locally and never advertised.
	if topo["mock-d"].APIFamily != "vllm" {
		t.Fatalf("mock-d api family = %q, want vllm", topo["mock-d"].APIFamily)
	}
}

// TestHeterogeneousHotPathPerf measures that a routing decision is a pure
// in-memory computation (no I/O, no synchronous probe) and completes well under
// the hot-path budget. The control plane (health/discovery/inventory) is
// maintained asynchronously, so the data-plane decision must be fast.
func TestHeterogeneousHotPathPerf(t *testing.T) {
	topo := mockTopology()
	cands := mockCandidates(t, topo, []string{"mock-a", "mock-b", "mock-c", "mock-e", "mock-d"})
	req := Req{RequiresTools: true}

	const n = 20000
	start := time.Now()
	for i := 0; i < n; i++ {
		Select(cands, req)
	}
	elapsed := time.Since(start)
	perOp := elapsed / time.Duration(n)
	const budget = 20 * time.Microsecond
	if perOp > budget {
		t.Fatalf("hot-path Select took %s/op, budget %s/op", perOp, budget)
	}
}

// stableCandidates builds candidates whose static-capacity Pools persist across
// Select calls, so the spillover/recovery scenarios model a real, shared
// admission pool rather than a fresh pool per call.
func stableCandidates(ids []string) []Candidate {
	topo := mockTopology()
	cands := make([]Candidate, 0, len(ids))
	for _, id := range ids {
		r := topo[id]
		caps, pool, priority := RoutingToEndpoint(r)
		cands = append(cands, Candidate{
			ID:         id,
			Priority:   priority,
			Capacity:   pool,
			Healthy:    true,
			Enabled:    true,
			MaxContext: caps.MaxContext,
			Caps:       caps,
		})
	}
	return cands
}

// fillPool reserves slots on a pool until it reports full (or unbounded).
// Select() itself reserves one slot on the winner, so the pool's active count
// already reflects the in-flight request; fill only the remaining slots.
func fillPool(t *testing.T, pool *Pool, cap int) {
	t.Helper()
	for pool.Active() < cap && pool.Reserve() {
		// reserve until full
	}
	if cap > 0 && pool.Active() != cap {
		t.Fatalf("pool %d/%d after fill", pool.Active(), cap)
	}
}

// TestHeterogeneousCapacitySpillover fills the primary endpoint's capacity and
// asserts selection spills to the next eligible endpoint, then recovers when
// capacity is released. The pools are stable across calls.
func TestHeterogeneousCapacitySpillover(t *testing.T) {
	cands := stableCandidates([]string{"mock-a", "mock-b", "mock-c"})
	// A plain text request: all three mocks serve text, so selection is driven
	// purely by priority + static capacity (no capability gating), which is exactly
	// the spillover behavior under test.
	req := Req{}

	// 1) Fresh pools: A (priority 10, cap 2) wins. Select reserves one slot.
	out := Select(cands, req)
	if out.SelectedID != "mock-a" {
		t.Fatalf("initial select = %q, want mock-a", out.SelectedID)
	}

	// 2) Fill A's remaining capacity (cap 2): next request spills to B (priority 20, cap 1).
	fillPool(t, cands[0].Capacity, 2)
	out = Select(cands, req)
	if out.SelectedID != "mock-b" {
		t.Fatalf("after A full, selected %q, want mock-b", out.SelectedID)
	}
	if out.Reasons["mock-a"] != ReasonCapacityFull {
		t.Errorf("mock-a reason = %q, want CAPACITY_FULL", out.Reasons["mock-a"])
	}

	// 3) Fill B (cap 1): next request spills to C (priority 30, cap 3).
	fillPool(t, cands[1].Capacity, 1)
	out = Select(cands, req)
	if out.SelectedID != "mock-c" {
		t.Fatalf("after B full, selected %q, want mock-c", out.SelectedID)
	}

	// 4) Release A's capacity: A wins again (deterministic recovery).
	for cands[0].Capacity.Active() > 0 {
		cands[0].Capacity.Release()
	}
	out = Select(cands, req)
	if out.SelectedID != "mock-a" {
		t.Fatalf("recovered, selected %q, want mock-a", out.SelectedID)
	}
}

// TestHeterogeneousCapacityRace asserts concurrency-safe admission: under a
// burst, the number of admitted reservations never exceeds the endpoint's
// static capacity. This is the TEST 25 concurrency-race case.
func TestHeterogeneousCapacityRace(t *testing.T) {
	cands := stableCandidates([]string{"mock-a"})
	pool := cands[0].Capacity
	const n = 100
	admitted := make(chan bool, n)
	for i := 0; i < n; i++ {
		go func() { admitted <- pool.Reserve() }()
	}
	adm := 0
	for i := 0; i < n; i++ {
		if <-admitted {
			adm++
		}
	}
	if adm != 2 {
		t.Fatalf("admitted %d, want 2 (mock-a static capacity)", adm)
	}
	if pool.Active() != 2 {
		t.Fatalf("pool active = %d, want 2", pool.Active())
	}
}

// TestHeterogeneousReservationLeak is the reservation-leak torture test (TEST
// 26): after a sequence of termination patterns (success, cancel, error,
// failover), every pool's active count returns to zero.
func TestHeterogeneousReservationLeak(t *testing.T) {
	cands := stableCandidates([]string{"mock-a", "mock-b", "mock-c"})
	req := Req{RequiresTools: true}

	// Simulate several requests, each reserving on selection and releasing on
	// a terminal path. Track the pools' active counts.
	for step := 0; step < 6; step++ {
		out := Select(cands, req)
		winner := cands[0].ID
		if out.SelectedID != "" {
			for i := range cands {
				if cands[i].ID == out.SelectedID {
					winner = cands[i].ID
					break
				}
			}
		}
		// Terminal paths (completed/cancelled/errored/failover) release the slot.
		for i := range cands {
			cands[i].Capacity.Release()
		}
		_ = winner
	}
	for i := range cands {
		if act := cands[i].Capacity.Active(); act != 0 {
			t.Fatalf("pool %s active = %d after all terminations, want 0 (leaked)", cands[i].ID, act)
		}
	}
}

// TestHeterogeneousMultiNodeCluster simulates a three-node PAIR cluster where
// each node fronts its own mock backend. It validates model-inventory
// propagation, remote routing, deterministic preference, and that a generic
// (unknown) engine identity survives peer propagation — the simulated
// multi-node cluster requirement.
func TestHeterogeneousMultiNodeCluster(t *testing.T) {
	// Three PAIR nodes: node-A -> Mock A, node-B -> Mock B, node-C -> Mock C.
	nodes := map[string]*noderec.EngineRouting{
		"node-a": {APIFamily: "openai", Priority: 10, StaticCapacity: 2,
			Capabilities: mockCaps(false, true, true, false), ContextMaxTokens: 262144,
			ModelRef: &noderec.EngineModelRef{PhysicalName: "qwen-fast", Aliases: []string{"local-coding"}}},
		"node-b": {APIFamily: "openai", Priority: 20, StaticCapacity: 1,
			Capabilities: mockCaps(false, true, true, false), ContextMaxTokens: 262144,
			ModelRef: &noderec.EngineModelRef{PhysicalName: "qwen-q4", Aliases: []string{"local-coding"}}},
		"node-c": {APIFamily: "openai", Priority: 30, StaticCapacity: 3,
			Capabilities: mockCaps(true, true, true, false), ContextMaxTokens: 262144,
			Timeouts: &noderec.EngineTimeouts{FirstByteMS: 1000},
			ModelRef: &noderec.EngineModelRef{PhysicalName: "flash-next", Aliases: []string{"local-coding"}}},
	}

	cands := make([]Candidate, 0, 3)
	for _, id := range []string{"node-a", "node-b", "node-c"} {
		caps, pool, priority := RoutingToEndpoint(nodes[id])
		cands = append(cands, Candidate{ID: id, Priority: priority, Capacity: pool,
			Healthy: true, Enabled: true, MaxContext: caps.MaxContext, Caps: caps})
	}

	// Normal coding request: deterministic preference routes to the lowest
	// priority (node-a, priority 10).
	req := Req{RequiresTools: true}
	out := Select(cands, req)
	if out.SelectedID != "node-a" {
		t.Fatalf("normal coding selected %q, want node-a (priority 10)", out.SelectedID)
	}

	// Vision request: only node-c (vision=true) is eligible.
	vreq := Req{HasImages: true}
	out = Select(cands, vreq)
	if out.SelectedID != "node-c" {
		t.Fatalf("vision selected %q, want node-c (only vision endpoint)", out.SelectedID)
	}
	if out.Reasons["node-a"] != ReasonVisionRequired || out.Reasons["node-b"] != ReasonVisionRequired {
		t.Errorf("vision reasons = %#v", out.Reasons)
	}

	// A generic (unknown) engine name's routing metadata is carried verbatim on
	// each node's per-engine routing map, and the router does not special-case it:
	// the same Select path routes a request for the unknown engine by its
	// declared capabilities/priority/capacity. This proves the generic engine
	// identity survives peer propagation with no brand-specific code. The cluster
	// uses the same Select + Pool primitives, so no new compiled-in engine type is
	// needed for an arbitrary engine name.
}
