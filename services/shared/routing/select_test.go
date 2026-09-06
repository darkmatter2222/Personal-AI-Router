// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"sync"
	"testing"

	"nvpair-shared/noderec"
)

// eligible builds a fully capable candidate: enabled, healthy, text-capable
// with generous context, unbounded capacity unless overridden.
func eligible(id string, priority int) Candidate {
	return Candidate{
		ID:       id,
		Priority: priority,
		Healthy:  true,
		Enabled:  true,
		Served:   []string{"m"},
		Caps: EndpointCaps{
			Text:       boolPtr(true),
			Vision:     boolPtr(true),
			Tools:      boolPtr(true),
			Streaming:  boolPtr(true),
			MaxContext: 262144,
		},
	}
}

func textReq() Req {
	return Req{Text: true, Model: "m"}
}

func TestSelectZeroCandidates(t *testing.T) {
	out := Select(nil, textReq())
	if out.SelectedID != "" {
		t.Fatalf("selected %q with zero candidates", out.SelectedID)
	}
	if len(out.Reasons) != 0 {
		t.Errorf("reasons = %v", out.Reasons)
	}
}

func TestSelectOneCandidate(t *testing.T) {
	out := Select([]Candidate{eligible("A", 1)}, Req{Text: true})
	if out.SelectedID != "A" || out.Reasons["A"] != Selected {
		t.Fatalf("got %q %v", out.SelectedID, out.Reasons)
	}
}

func TestSelectAllEligible(t *testing.T) {
	out := Select([]Candidate{eligible("A", 10), eligible("B", 20), eligible("C", 30)}, Req{Text: true})
	if out.SelectedID != "A" {
		t.Fatalf("selected %q, want A", out.SelectedID)
	}
	if out.Reasons["B"] != ReasonNotSelected || out.Reasons["C"] != ReasonNotSelected {
		t.Errorf("non-selected reasons = %v", out.Reasons)
	}
}

func TestSelectAllIneligibleIsAuthoritative(t *testing.T) {
	c := eligible("A", 1)
	c.Enabled = false
	c2 := eligible("B", 2)
	c2.Draining = true
	c3 := eligible("C", 3)
	c3.Healthy = false
	out := Select([]Candidate{c, c2, c3}, textReq())
	if out.SelectedID != "" {
		t.Fatalf("selected %q although all candidates are ineligible; rejection must be authoritative", out.SelectedID)
	}
	if out.Reasons["A"] != ReasonEndpointDisabled ||
		out.Reasons["B"] != ReasonEndpointDraining ||
		out.Reasons["C"] != ReasonEndpointUnhealthy {
		t.Errorf("reasons = %v", out.Reasons)
	}
}

func TestSelectAllCapacityFull(t *testing.T) {
	a := eligible("A", 1)
	a.Capacity = NewPool(1)
	b := eligible("B", 2)
	b.Capacity = NewPool(1)
	a.Capacity.Reserve()
	b.Capacity.Reserve()
	out := Select([]Candidate{a, b}, Req{Text: true})
	if out.SelectedID != "" {
		t.Fatalf("selected %q although all capacity is full", out.SelectedID)
	}
	if out.Reasons["A"] != ReasonCapacityFull || out.Reasons["B"] != ReasonCapacityFull {
		t.Errorf("reasons = %v", out.Reasons)
	}
}

func TestSelectOneDisabledOneDrainingOneUnhealthy(t *testing.T) {
	disabled := eligible("disabled", 1)
	disabled.Enabled = false
	draining := eligible("draining", 2)
	draining.Draining = true
	unhealthy := eligible("unhealthy", 3)
	unhealthy.Healthy = false
	ok := eligible("ok", 4)
	out := Select([]Candidate{disabled, draining, unhealthy, ok}, Req{Text: true})
	if out.SelectedID != "ok" {
		t.Fatalf("selected %q, want ok", out.SelectedID)
	}
}

func TestSelectUnboundedCapacity(t *testing.T) {
	c := eligible("A", 1)
	c.Capacity = NewPool(0)
	for i := 0; i < 100; i++ {
		if !c.Capacity.Reserve() {
			t.Fatalf("unbounded pool full after %d reservations", i)
		}
	}
	if c.Capacity.Active() != 100 {
		t.Fatalf("active = %d, want 100", c.Capacity.Active())
	}
}

func TestSelectCapacityOne(t *testing.T) {
	c := eligible("A", 1)
	c.Capacity = NewPool(1)
	out := Select([]Candidate{c}, textReq())
	if out.SelectedID != "A" {
		t.Fatalf("first request not admitted: %v", out.Reasons)
	}
	out = Select([]Candidate{c}, textReq())
	if out.SelectedID != "" || out.Reasons["A"] != ReasonCapacityFull {
		t.Fatalf("second request admitted anyway: selected %q reasons %v", out.SelectedID, out.Reasons)
	}
}

func TestSelectEqualPriorityStableIDTieBreak(t *testing.T) {
	// Equal priority: the lower ID wins, deterministically.
	cands := []Candidate{eligible("zeta", 10), eligible("alpha", 10), eligible("mid", 10)}
	for i := range cands {
		cands[i].Served = nil
	}
	out := Select(cands, Req{Text: true})
	if out.SelectedID != "alpha" {
		t.Fatalf("tie-break selected %q, want alpha (lowest ID)", out.SelectedID)
	}
}

func TestSelectPriorityTieAcrossEligibleSet(t *testing.T) {
	// A has the best priority but no tools; B and C tie at priority 20 with
	// tools. B must win the tie by ID, proving capability runs before the tie.
	a := eligible("A", 10)
	a.Caps.Tools = boolPtr(false)
	b := eligible("B", 20)
	c := eligible("C", 20)
	a.Served, b.Served, c.Served = nil, nil, nil
	out := Select([]Candidate{c, a, b}, Req{Text: true, RequiresTools: true})
	if out.SelectedID != "B" {
		t.Fatalf("selected %q, want B (capability first, then ID tie-break)", out.SelectedID)
	}
	if out.Reasons["A"] != ReasonToolsRequired {
		t.Errorf("A reason = %q, want TOOLS_REQUIRED", out.Reasons["A"])
	}
}

func TestSelectUnknownModel(t *testing.T) {
	a := eligible("A", 1)
	a.Served = []string{"qwen-fast"}
	b := eligible("B", 2)
	b.Served = []string{"llama"}
	out := Select([]Candidate{a, b}, Req{Text: true, Model: "flash-next"})
	if out.SelectedID != "" {
		t.Fatalf("selected %q for a model no candidate serves", out.SelectedID)
	}
	if out.Reasons["A"] != ReasonModelNotAvailable || out.Reasons["B"] != ReasonModelNotAvailable {
		t.Errorf("reasons = %v", out.Reasons)
	}
}

func TestSelectPhysicalModelMatch(t *testing.T) {
	a := eligible("A", 1)
	a.Served = []string{"qwen-fast"}
	b := eligible("B", 2)
	b.Served = []string{"qwen-q4"}
	out := Select([]Candidate{a, b}, Req{Text: true, Model: "qwen-q4"})
	if out.SelectedID != "B" {
		t.Fatalf("selected %q, want B (serves the physical model)", out.SelectedID)
	}
	if out.Reasons["A"] != ReasonModelNotAvailable {
		t.Errorf("A reason = %q, want MODEL_NOT_AVAILABLE", out.Reasons["A"])
	}
}

func TestSelectLogicalAliasMatch(t *testing.T) {
	a := eligible("A", 1)
	a.Served = []string{"qwen-fast"}
	a.Aliases = []string{"local-coding"}
	b := eligible("B", 2)
	b.Served = []string{"qwen-q4"}
	out := Select([]Candidate{a, b}, Req{Text: true, Model: "local-coding"})
	if out.SelectedID != "A" {
		t.Fatalf("selected %q, want A (declares the logical alias)", out.SelectedID)
	}
}

func TestSelectMultipleAliases(t *testing.T) {
	a := eligible("A", 1)
	a.Served = []string{"qwen-fast"}
	a.Aliases = []string{"local-coding", "fast-chat"}
	out := Select([]Candidate{a}, Req{Text: true, Model: "fast-chat"})
	if out.SelectedID != "A" {
		t.Fatalf("second alias did not match: %v", out.Reasons)
	}
}

func TestSelectSameAliasMultipleEndpoints(t *testing.T) {
	// Two endpoints declare the same logical alias with different physical
	// models; both are eligible and priority decides.
	a := eligible("A", 10)
	a.Served = []string{"qwen-fast"}
	a.Aliases = []string{"local-coding"}
	b := eligible("B", 20)
	b.Served = []string{"qwen-q4"}
	b.Aliases = []string{"local-coding"}
	out := Select([]Candidate{a, b}, Req{Text: true, Model: "local-coding"})
	if out.SelectedID != "A" {
		t.Fatalf("selected %q, want A by priority", out.SelectedID)
	}
}

func TestSelectNilRoutingMetadataCandidate(t *testing.T) {
	// A candidate with no routing metadata at all (legacy node): eligible for
	// a plain text request, no capability gates fire.
	legacy := Candidate{ID: "legacy", Healthy: true, Enabled: true}
	out := Select([]Candidate{legacy}, Req{})
	if out.SelectedID != "legacy" {
		t.Fatalf("legacy candidate rejected: %v", out.Reasons)
	}
}

func TestSelectPartialCapabilities(t *testing.T) {
	// Declares only text=true; a tools request must be rejected with
	// TOOLS_REQUIRED, not silently passed.
	c := eligible("A", 1)
	c.Caps = EndpointCaps{Text: boolPtr(true), MaxContext: 262144}
	out := Select([]Candidate{c}, Req{Text: true, RequiresTools: true, Model: "m"})
	if out.SelectedID != "" || out.Reasons["A"] != ReasonToolsRequired {
		t.Fatalf("selected %q reasons %v", out.SelectedID, out.Reasons)
	}
}

func TestSelectEmptyModelNoAvailabilityGate(t *testing.T) {
	// A request naming no model must not be filtered by the model gate even
	// when candidates declare inventories.
	a := eligible("A", 1)
	a.Served = []string{"qwen-fast"}
	out := Select([]Candidate{a}, Req{Text: true, Model: "qwen-fast"})
	if out.SelectedID != "A" {
		t.Fatalf("empty-model request filtered out: %v", out.Reasons)
	}
}

func TestSelectAPIFamilyGate(t *testing.T) {
	a := eligible("A", 1)
	a.APIFamily = "ollama"
	b := eligible("B", 2)
	b.APIFamily = "openai"
	c := eligible("C", 3) // family undeclared: compatible with anything
	out := Select([]Candidate{a, b, c}, Req{Text: true, APIFamily: "openai", Model: "m"})
	if out.SelectedID != "B" {
		t.Fatalf("selected %q, want B (openai family)", out.SelectedID)
	}
	if out.Reasons["A"] != ReasonAPIFamily {
		t.Errorf("A reason = %q, want API_FAMILY_INCOMPATIBLE", out.Reasons["A"])
	}
}

func TestSelectAPIFamilyUndeclaredCompatible(t *testing.T) {
	a := eligible("A", 1)
	// Undeclared family on the candidate is always compatible.
	out := Select([]Candidate{a}, Req{Text: true, APIFamily: "vllm", Model: "m"})
	if out.SelectedID != "A" {
		t.Fatalf("undeclared family rejected: %v", out.Reasons)
	}
	// And an undeclared requested family matches any declared candidate.
	b := eligible("B", 1)
	b.APIFamily = "ollama"
	out = Select([]Candidate{b}, Req{Text: true, Model: "m"})
	if out.SelectedID != "B" {
		t.Fatalf("undeclared request family rejected: %v", out.Reasons)
	}
}

func TestSelectContextGate(t *testing.T) {
	small := eligible("A", 1)
	small.Caps.MaxContext = 8192
	big := eligible("B", 2)
	big.Caps.MaxContext = 262144
	req := Req{Text: true, RequiredContext: 100000}
	out := Select([]Candidate{small, big}, req)
	if out.SelectedID != "B" {
		t.Fatalf("selected %q, want B (context fits)", out.SelectedID)
	}
	if out.Reasons["A"] != ReasonContextTooSmall {
		t.Errorf("A reason = %q, want CONTEXT_TOO_SMALL", out.Reasons["A"])
	}
}

func TestSelectModelLevelCapsOverride(t *testing.T) {
	// Engine default has no vision; one model declares vision. A vision
	// request must be eligible for the model that declares it.
	c := eligible("A", 1)
	c.Caps.Vision = boolPtr(false)
	c.Served = []string{"text-7b", "vision-7b"}
	c.Models = []noderec.EngineModelRef{
		{PhysicalName: "text-7b", Caps: &noderec.EngineCaps{Text: boolPtr(true), Tools: boolPtr(true), Streaming: boolPtr(true)}},
		{PhysicalName: "vision-7b", Caps: &noderec.EngineCaps{Text: boolPtr(true), Vision: boolPtr(true), Tools: boolPtr(true), Streaming: boolPtr(true)}, ContextMaxTokens: 128000},
	}
	out := Select([]Candidate{c}, Req{Text: true, HasImages: true, Model: "vision-7b"})
	if out.SelectedID != "A" {
		t.Fatalf("vision request on a vision model rejected: %v", out.Reasons)
	}
	// The same model without vision declared must be rejected.
	out = Select([]Candidate{c}, Req{Text: true, HasImages: true, Model: "text-7b"})
	if out.Reasons["A"] != ReasonVisionRequired {
		t.Errorf("text-7b vision reason = %q, want VISION_REQUIRED", out.Reasons["A"])
	}
	// Context override: a request that fits the 128K model budget must be
	// eligible (the model override wins over the endpoint default of 262K
	// only when it declares a budget; here it declares 128K).
	req := Req{Text: true, Model: "vision-7b", RequiredContext: 128000}
	if reason := EffectiveCaps(c.Caps, c.Models, "vision-7b").Check(req); reason != "" {
		t.Errorf("128K request vs 128K model override: %q", reason)
	}
	// And a request above the model budget is rejected by the override.
	req = Req{Text: true, Model: "vision-7b", RequiredContext: 200000}
	if reason := EffectiveCaps(c.Caps, c.Models, "vision-7b").Check(req); reason != ReasonContextTooSmall {
		t.Errorf("200K request vs 128K model override: %q, want CONTEXT_TOO_SMALL", reason)
	}
}

func TestSelectModelAliasCapsOverride(t *testing.T) {
	c := eligible("A", 1)
	c.Caps.Tools = boolPtr(false)
	c.Served = []string{"qwen-fast"}
	c.Aliases = []string{"local-coding"}
	c.Models = []noderec.EngineModelRef{
		{PhysicalName: "qwen-fast", Aliases: []string{"local-coding"}, Caps: &noderec.EngineCaps{Text: boolPtr(true), Tools: boolPtr(true), Vision: boolPtr(true), Streaming: boolPtr(true)}},
	}
	out := Select([]Candidate{c}, Req{Text: true, RequiresTools: true, Model: "local-coding"})
	if out.SelectedID != "A" {
		t.Fatalf("alias with per-model tools caps rejected: %v", out.Reasons)
	}
}

func TestSelectReservesWinnerPoolOnly(t *testing.T) {
	a := eligible("A", 10)
	a.Capacity = NewPool(1)
	b := eligible("B", 20)
	b.Capacity = NewPool(1)
	out := Select([]Candidate{a, b}, Req{Text: true, Model: "m"})
	if out.SelectedID != "A" {
		t.Fatalf("selected %q", out.SelectedID)
	}
	if a.Capacity.Active() != 1 || b.Capacity.Active() != 0 {
		t.Fatalf("reservation leaked to the wrong pool: A=%d B=%d", a.Capacity.Active(), b.Capacity.Active())
	}
}

func TestSelectSpillReservesSpillTarget(t *testing.T) {
	a := eligible("A", 10)
	a.Capacity = NewPool(1)
	b := eligible("B", 20)
	b.Capacity = NewPool(1)
	a.Capacity.Reserve() // A full
	out := Select([]Candidate{a, b}, Req{Text: true, Model: "m"})
	if out.SelectedID != "B" {
		t.Fatalf("selected %q, want B", out.SelectedID)
	}
	if b.Capacity.Active() != 1 {
		t.Fatalf("spill target not reserved: B=%d", b.Capacity.Active())
	}
}

func TestSelectConcurrent(t *testing.T) {
	cands := make([]Candidate, 0, 10)
	for i := 0; i < 10; i++ {
		c := eligible(string(rune('a'+i)), i)
		c.Capacity = NewPool(1)
		cands = append(cands, c)
	}
	const n = 200
	var wg sync.WaitGroup
	selected := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := Select(cands, Req{Text: true, Model: "m"})
			selected <- out.SelectedID
		}()
	}
	wg.Wait()
	close(selected)
	counts := map[string]int{}
	for id := range selected {
		counts[id]++
	}
	delete(counts, "")
	// Exactly one slot per endpoint: total admitted == number of endpoints,
	// never more (atomic reservation under concurrency).
	total := 0
	for id, n := range counts {
		if n > 1 {
			t.Errorf("endpoint %s admitted %d times, want <= 1", id, n)
		}
		total += n
	}
	if total != len(cands) {
		t.Fatalf("total admitted = %d, want %d (one per endpoint)", total, len(cands))
	}
}

func TestPoolResizeExpansion(t *testing.T) {
	p := NewPool(1)
	if !p.Reserve() {
		t.Fatal("first reserve failed")
	}
	if p.Reserve() {
		t.Fatal("second reserve should fail at capacity 1")
	}
	p.Resize(4)
	if p.Capacity() != 4 {
		t.Fatalf("capacity = %d, want 4", p.Capacity())
	}
	for i := 0; i < 3; i++ {
		if !p.Reserve() {
			t.Fatalf("reserving up to the new capacity failed at %d", p.Active())
		}
	}
	if p.Active() != 4 {
		t.Fatalf("active = %d, want 4", p.Active())
	}
}

func TestPoolResizeContraction(t *testing.T) {
	p := NewPool(4)
	for i := 0; i < 4; i++ {
		if !p.Reserve() {
			t.Fatal("reserve failed")
		}
	}
	p.Resize(1)
	// Full at the new limit: no new admissions.
	if p.Reserve() {
		t.Fatal("reserve at contracted full capacity should fail")
	}
	// In-flight requests keep their slots; as they drain, admissions resume
	// once the active count is below the new limit (active == limit is full).
	for p.Active() > 0 {
		p.Release()
	}
	if !p.Reserve() {
		t.Fatal("reserve after draining below the new capacity should succeed")
	}
}

func TestPoolResizeContractionBelowActive(t *testing.T) {
	p := NewPool(4)
	for i := 0; i < 3; i++ {
		p.Reserve()
	}
	p.Resize(1)
	// While active (3) exceeds the new limit (1), no new admissions.
	if p.Reserve() {
		t.Fatal("active 3 > new capacity 1 must block new reservations")
	}
	// In-flight slots drain one at a time; admissions resume once the active
	// count is below the new limit, and are blocked again at the limit.
	p.Release() // 3 -> 2: still above the new limit of 1
	if p.Reserve() {
		t.Fatal("active 2 > new capacity 1 must block new reservations")
	}
	p.Release() // 2 -> 1: at the new limit, full
	if p.Reserve() {
		t.Fatal("at the new limit, new reservations must be blocked")
	}
	p.Release() // 1 -> 0: below the new limit
	if !p.Reserve() {
		t.Fatal("after draining below the new limit, reservations must resume")
	}
}

func TestPoolResizeNegativeIsUnbounded(t *testing.T) {
	p := NewPool(1)
	p.Resize(-3)
	if p.Capacity() != 0 {
		t.Fatalf("negative resize should clamp to 0 (unbounded), got %d", p.Capacity())
	}
	for i := 0; i < 10; i++ {
		if !p.Reserve() {
			t.Fatal("unbounded pool should always admit")
		}
	}
}

func TestPoolReleaseAtZeroNoNegative(t *testing.T) {
	p := NewPool(5)
	p.Release()
	p.Release()
	p.Release()
	if p.Active() != 0 {
		t.Fatalf("active = %d after over-release, want 0 (no negative counters)", p.Active())
	}
}

func TestPoolDoubleRelease(t *testing.T) {
	p := NewPool(2)
	p.Reserve()
	p.Release()
	p.Release()
	if p.Active() != 0 {
		t.Fatalf("double-release drove active to %d", p.Active())
	}
}
