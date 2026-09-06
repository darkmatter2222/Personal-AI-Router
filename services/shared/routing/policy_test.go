// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"reflect"
	"testing"
)

// eligibleEndpoint builds a healthy, text-capable endpoint serving "logical".
func eligibleEndpoint(id string, priority int, caps Capabilities) Endpoint {
	return Endpoint{
		ID:        id,
		Engine:    "e",
		APIFamily: APIFamilyOpenAI,
		Priority:  priority,
		Healthy:   true,
		Models:    []ModelRouting{{Physical: "phys-" + id, Aliases: []string{"logical"}, Capabilities: caps}},
	}
}

func orderedIDs(d Decision) []string {
	ids := make([]string, len(d.Ordered))
	for i, p := range d.Ordered {
		ids[i] = p.EndpointID
	}
	return ids
}

func textReq() Requirements {
	return Requirements{Model: "logical", APIFamily: APIFamilyOpenAI}
}

func TestDecide_ZeroCandidates(t *testing.T) {
	d := Decide(textReq(), nil, StrategyDeterministicPriority, nil)
	if len(d.Ordered) != 0 || len(d.Rejected) != 0 {
		t.Fatalf("empty input should give empty decision: %+v", d)
	}
	if _, ok := d.Selected(); ok {
		t.Fatal("Selected must be false for empty decision")
	}
}

func TestDecide_DeterministicPriorityOrder(t *testing.T) {
	full := Capabilities{Text: true}
	eps := []Endpoint{
		eligibleEndpoint("C", 30, full),
		eligibleEndpoint("A", 10, full),
		eligibleEndpoint("B", 20, full),
	}
	d := Decide(textReq(), eps, StrategyDeterministicPriority, nil)
	if got := orderedIDs(d); !reflect.DeepEqual(got, []string{"A", "B", "C"}) {
		t.Fatalf("priority order = %v, want [A B C]", got)
	}
	sel, ok := d.Selected()
	if !ok || sel.EndpointID != "A" || sel.Physical != "phys-A" {
		t.Fatalf("selected = %+v ok=%v; want A phys-A", sel, ok)
	}
}

func TestDecide_PriorityTieBreakByID(t *testing.T) {
	full := Capabilities{Text: true}
	eps := []Endpoint{
		eligibleEndpoint("z", 10, full),
		eligibleEndpoint("a", 10, full),
		eligibleEndpoint("m", 10, full),
	}
	d := Decide(textReq(), eps, StrategyDeterministicPriority, nil)
	if got := orderedIDs(d); !reflect.DeepEqual(got, []string{"a", "m", "z"}) {
		t.Fatalf("tie-break order = %v, want [a m z]", got)
	}
}

func TestDecide_DefaultStrategyUsesSchedulerOrder(t *testing.T) {
	full := Capabilities{Text: true}
	eps := []Endpoint{
		eligibleEndpoint("A", 10, full),
		eligibleEndpoint("B", 99, full),
		eligibleEndpoint("C", 1, full),
	}
	// Scheduler prefers B then A; C not ranked -> last. Priority is IGNORED under
	// the default strategy.
	d := Decide(textReq(), eps, StrategyDefault, []string{"B", "A"})
	if got := orderedIDs(d); !reflect.DeepEqual(got, []string{"B", "A", "C"}) {
		t.Fatalf("default order = %v, want [B A C]", got)
	}
}

func TestDecide_DefaultUnrankedSortByID(t *testing.T) {
	full := Capabilities{Text: true}
	eps := []Endpoint{
		eligibleEndpoint("y", 1, full),
		eligibleEndpoint("x", 1, full),
	}
	d := Decide(textReq(), eps, StrategyDefault, nil) // none ranked
	if got := orderedIDs(d); !reflect.DeepEqual(got, []string{"x", "y"}) {
		t.Fatalf("unranked order = %v, want [x y]", got)
	}
}

func TestDecide_AllIneligible(t *testing.T) {
	// A tools request; no endpoint supports tools -> all rejected, nothing
	// ordered. This is the authoritative no-eligible-endpoint result.
	eps := []Endpoint{
		eligibleEndpoint("A", 10, Capabilities{Text: true, Tools: false}),
		eligibleEndpoint("B", 20, Capabilities{Text: true, Tools: false}),
	}
	req := Requirements{Model: "logical", APIFamily: APIFamilyOpenAI, RequiresTools: true}
	d := Decide(req, eps, StrategyDeterministicPriority, nil)
	if len(d.Ordered) != 0 {
		t.Fatalf("expected nothing eligible, got %v", orderedIDs(d))
	}
	if len(d.Rejected) != 2 {
		t.Fatalf("expected 2 rejections, got %d", len(d.Rejected))
	}
	for _, r := range d.Rejected {
		if r.Reason != ReasonToolsRequired {
			t.Fatalf("reason = %q, want TOOLS_REQUIRED", r.Reason)
		}
	}
}

func TestDecide_EligibilityBeforeScheduling(t *testing.T) {
	// The scheduler prefers A (idle, first in defaultOrder) but A cannot do
	// tools; the capable B must be selected despite being scheduler-second.
	eps := []Endpoint{
		eligibleEndpoint("A", 10, Capabilities{Text: true, Tools: false}),
		eligibleEndpoint("B", 20, Capabilities{Text: true, Tools: true}),
	}
	req := Requirements{Model: "logical", APIFamily: APIFamilyOpenAI, RequiresTools: true}
	// Default strategy, scheduler ranks A first.
	d := Decide(req, eps, StrategyDefault, []string{"A", "B"})
	sel, ok := d.Selected()
	if !ok || sel.EndpointID != "B" {
		t.Fatalf("capable B must win over scheduler-preferred A; selected=%+v ok=%v", sel, ok)
	}
	// And under deterministic priority the lower-priority-number A would be
	// preferred if eligible, but it isn't, so B still wins.
	d2 := Decide(req, eps, StrategyDeterministicPriority, nil)
	sel2, _ := d2.Selected()
	if sel2.EndpointID != "B" {
		t.Fatalf("capable B must win; selected=%+v", sel2)
	}
}

func TestDecide_PlacementCarriesFields(t *testing.T) {
	e := Endpoint{
		ID: "A", Engine: "custom", APIFamily: APIFamilyOpenAI, Priority: 5, Capacity: 4,
		Healthy: true, Timeouts: Timeouts{FirstByteMS: 30000},
		Models: []ModelRouting{{Physical: "real-model", Aliases: []string{"logical"}, Capabilities: Capabilities{Text: true}}},
	}
	d := Decide(textReq(), []Endpoint{e}, StrategyDeterministicPriority, nil)
	p, ok := d.Selected()
	if !ok {
		t.Fatal("expected a selection")
	}
	if p.Physical != "real-model" || p.Engine != "custom" || p.Capacity != 4 || p.Timeouts.FirstByteMS != 30000 {
		t.Fatalf("placement missing fields: %+v", p)
	}
}

func TestResolveStrategy(t *testing.T) {
	def := []Endpoint{{Strategy: StrategyDefault}, {Strategy: StrategyDefault}}
	if got := ResolveStrategy(def); got != StrategyDefault {
		t.Fatalf("all default -> %q", got)
	}
	mixed := []Endpoint{{Strategy: StrategyDefault}, {Strategy: StrategyDeterministicPriority}}
	if got := ResolveStrategy(mixed); got != StrategyDeterministicPriority {
		t.Fatalf("any deterministic must dominate, got %q", got)
	}
	if got := ResolveStrategy(nil); got != StrategyDefault {
		t.Fatalf("empty -> default, got %q", got)
	}
}

func TestDecide_RecordsStrategy(t *testing.T) {
	d := Decide(textReq(), []Endpoint{eligibleEndpoint("A", 1, Capabilities{Text: true})}, StrategyDeterministicPriority, nil)
	if d.Strategy != StrategyDeterministicPriority {
		t.Fatalf("decision should record its strategy, got %q", d.Strategy)
	}
}
