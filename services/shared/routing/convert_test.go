// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"testing"

	"nvpair-shared/noderec"
)

func TestRoutingToCandidateNil(t *testing.T) {
	c := RoutingToCandidate(nil, []string{"model-a"})
	if c.Enabled != true {
		t.Fatal("nil routing must default to enabled (legacy node)")
	}
	if c.Capacity == nil || c.Capacity.Capacity() != 0 {
		t.Fatal("nil routing must be unbounded")
	}
	if c.Priority != 0 || c.Strategy != "" || c.MaxContext != 0 {
		t.Fatalf("nil routing = %+v", c)
	}
	if len(c.Served) != 1 || c.Served[0] != "model-a" {
		t.Fatalf("served = %v", c.Served)
	}
}

func TestRoutingToCandidateDefaults(t *testing.T) {
	// Enabled omitted: defaults to enabled (the zero-value-inversion trap).
	c := RoutingToCandidate(&noderec.EngineRouting{}, nil)
	if c.Enabled != true {
		t.Fatal("omitted enabled must default to true")
	}
	if c.Capacity == nil || c.Capacity.Capacity() != 0 {
		t.Fatal("omitted capacity must be unbounded (0 = legacy)")
	}
	if c.Priority != 0 {
		t.Fatal("omitted priority must be 0 (lowest-preferred among defaults)")
	}
}

func TestRoutingToCandidateDisabled(t *testing.T) {
	off := false
	c := RoutingToCandidate(&noderec.EngineRouting{Enabled: &off}, nil)
	if c.Enabled {
		t.Fatal("explicit enabled=false must disable")
	}
	on := true
	c = RoutingToCandidate(&noderec.EngineRouting{Enabled: &on}, nil)
	if !c.Enabled {
		t.Fatal("explicit enabled=true must enable")
	}
}

func TestRoutingToCandidateDraining(t *testing.T) {
	c := RoutingToCandidate(&noderec.EngineRouting{Draining: true}, nil)
	if !c.Draining {
		t.Fatal("draining must be carried")
	}
}

func TestRoutingToCandidateFull(t *testing.T) {
	text, vision, tools, streaming, reasoning := true, true, true, true, true
	c := RoutingToCandidate(&noderec.EngineRouting{
		APIFamily:        "openai",
		Priority:         10,
		StaticCapacity:   2,
		ContextMaxTokens: 262144,
		Strategy:         StrategyDeterministic,
		Pool:             "pool-a",
		Timeouts:         &noderec.EngineTimeouts{ConnectMS: 250, ResponseHeaderMS: 30000, FirstByteMS: 60000},
		Capabilities: &noderec.EngineCaps{
			Text: &text, Vision: &vision, Tools: &tools, Streaming: &streaming, Reasoning: &reasoning,
		},
		ModelRef: &noderec.EngineModelRef{PhysicalName: "phys", Aliases: []string{"alias1", "alias2"}},
	}, []string{"phys", "other"})

	if c.APIFamily != "openai" {
		t.Errorf("api family = %q", c.APIFamily)
	}
	if c.Priority != 10 {
		t.Errorf("priority = %d", c.Priority)
	}
	if c.Capacity.Capacity() != 2 {
		t.Errorf("capacity = %d", c.Capacity.Capacity())
	}
	if c.MaxContext != 262144 {
		t.Errorf("context = %d", c.MaxContext)
	}
	if c.Strategy != StrategyDeterministic {
		t.Errorf("strategy = %q", c.Strategy)
	}
	if !boolOn(c.Caps.Text) || !boolOn(c.Caps.Vision) || !boolOn(c.Caps.Tools) ||
		!boolOn(c.Caps.Streaming) || !boolOn(c.Caps.Reasoning) {
		t.Errorf("caps = %+v", c.Caps)
	}
	if len(c.Served) != 2 {
		t.Errorf("served = %v", c.Served)
	}
	if len(c.Aliases) != 2 || c.Aliases[0] != "alias1" {
		t.Errorf("aliases = %v", c.Aliases)
	}
	if len(c.Models) != 1 || c.Models[0].PhysicalName != "phys" {
		t.Errorf("models = %+v", c.Models)
	}
}

func TestRoutingToCandidateServedIsCopy(t *testing.T) {
	src := []string{"a"}
	c := RoutingToCandidate(nil, src)
	src[0] = "mutated"
	if c.Served[0] != "a" {
		t.Fatal("served list must be copied, not shared with the input")
	}
}

func TestRoutingToCandidatePartialCaps(t *testing.T) {
	text := true
	c := RoutingToCandidate(&noderec.EngineRouting{
		Capabilities: &noderec.EngineCaps{Text: &text},
	}, nil)
	if !boolOn(c.Caps.Text) {
		t.Fatal("declared text capability lost")
	}
	if c.Caps.Vision != nil || c.Caps.Tools != nil || c.Caps.Streaming != nil {
		t.Fatal("undeclared capabilities must stay nil (unsupported)")
	}
}

func TestValidStrategy(t *testing.T) {
	if !ValidStrategy(StrategyDefault) || !ValidStrategy(StrategyDeterministic) {
		t.Fatal("known strategies must validate")
	}
	if ValidStrategy("") || ValidStrategy("bogus") {
		t.Fatal("unknown strategies must not validate")
	}
}
