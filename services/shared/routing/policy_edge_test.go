// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"reflect"
	"testing"
)

func logicalModel() []ModelRouting {
	return []ModelRouting{{Physical: "p", Aliases: []string{"logical"}, Capabilities: Capabilities{Text: true}}}
}

// TestDecide_OmittedPrioritySortsLast: an endpoint built with no priority
// resolves to DefaultPriority and sorts after any explicitly-prioritised one.
func TestDecide_OmittedPrioritySortsLast(t *testing.T) {
	explicit := (EngineRouting{Priority: intp(50), Models: logicalModel()}).Endpoint("explicit", "e", true)
	omitted := (EngineRouting{Models: logicalModel()}).Endpoint("omitted", "e", true)
	d := Decide(textReq(), []Endpoint{omitted, explicit}, StrategyDeterministicPriority, nil)
	if got := orderedIDs(d); !reflect.DeepEqual(got, []string{"explicit", "omitted"}) {
		t.Fatalf("omitted-priority endpoint must sort last, got %v", got)
	}
}

func TestDecide_DefaultStrategyIgnoresStaleOrderIDs(t *testing.T) {
	full := Capabilities{Text: true}
	eps := []Endpoint{eligibleEndpoint("A", 1, full), eligibleEndpoint("B", 1, full)}
	d := Decide(textReq(), eps, StrategyDefault, []string{"ghost", "B", "A"})
	if got := orderedIDs(d); !reflect.DeepEqual(got, []string{"B", "A"}) {
		t.Fatalf("a stale scheduler-order id must be ignored, got %v", got)
	}
}

func TestDecide_DefaultStrategyDuplicateOrderID(t *testing.T) {
	full := Capabilities{Text: true}
	eps := []Endpoint{eligibleEndpoint("A", 1, full), eligibleEndpoint("B", 1, full)}
	d := Decide(textReq(), eps, StrategyDefault, []string{"B", "A", "B"})
	if got := orderedIDs(d); !reflect.DeepEqual(got, []string{"B", "A"}) {
		t.Fatalf("a duplicated order id must use the first rank, got %v", got)
	}
}

func TestDecide_SingleIneligible(t *testing.T) {
	eps := []Endpoint{eligibleEndpoint("A", 1, Capabilities{Text: false})}
	d := Decide(textReq(), eps, StrategyDeterministicPriority, nil)
	if len(d.Ordered) != 0 || len(d.Rejected) != 1 || d.Rejected[0].Reason != ReasonTextRequired {
		t.Fatalf("single ineligible: %+v", d)
	}
}

// TestDecide_MixedExplanation checks the full routing explanation: one eligible
// endpoint ordered, and distinct stable reasons for each rejected one.
func TestDecide_MixedExplanation(t *testing.T) {
	req := Requirements{Model: "logical", APIFamily: APIFamilyOpenAI, RequiresTools: true, RequiredContext: 1000}
	eps := []Endpoint{
		{ID: "ok", Healthy: true, APIFamily: APIFamilyOpenAI, Priority: 1, Models: []ModelRouting{{Physical: "p", Aliases: []string{"logical"}, Capabilities: Capabilities{Text: true, Tools: true}, Context: Context{MaxTokens: 8192}}}},
		{ID: "notools", Healthy: true, APIFamily: APIFamilyOpenAI, Priority: 2, Models: []ModelRouting{{Physical: "p", Aliases: []string{"logical"}, Capabilities: Capabilities{Text: true, Tools: false}}}},
		{ID: "small", Healthy: true, APIFamily: APIFamilyOpenAI, Priority: 3, Models: []ModelRouting{{Physical: "p", Aliases: []string{"logical"}, Capabilities: Capabilities{Text: true, Tools: true}, Context: Context{MaxTokens: 100}}}},
		{ID: "off", Healthy: true, Disabled: true, APIFamily: APIFamilyOpenAI, Priority: 0, Models: []ModelRouting{{Physical: "p", Aliases: []string{"logical"}, Capabilities: Capabilities{Text: true, Tools: true}}}},
	}
	d := Decide(req, eps, StrategyDeterministicPriority, nil)
	if len(d.Ordered) != 1 || d.Ordered[0].EndpointID != "ok" {
		t.Fatalf("only ok should be eligible, got %v", orderedIDs(d))
	}
	reasons := map[string]Reason{}
	for _, r := range d.Rejected {
		reasons[r.EndpointID] = r.Reason
	}
	if reasons["notools"] != ReasonToolsRequired || reasons["small"] != ReasonContextTooSmall || reasons["off"] != ReasonEndpointDisabled {
		t.Fatalf("rejection reasons wrong: %+v", reasons)
	}
}

func TestResolveStrategy_DeterministicOnLast(t *testing.T) {
	eps := []Endpoint{{Strategy: StrategyDefault}, {Strategy: StrategyDefault}, {Strategy: StrategyDeterministicPriority}}
	if ResolveStrategy(eps) != StrategyDeterministicPriority {
		t.Fatal("deterministic-priority on the last endpoint must dominate")
	}
}
