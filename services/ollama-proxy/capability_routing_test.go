// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"testing"

	"nvpair-shared/noderec"
)

// routeInference component tests: capability-aware selection, capacity
// reservation, model availability, API family gating, and failover ordering.

func boolPtr(b bool) *bool { return &b }

func capText() *noderec.EngineCaps {
	b := true
	return &noderec.EngineCaps{Text: &b}
}

func inferenceBody(model string) []byte {
	b, _ := json.Marshal(map[string]any{"model": model, "messages": []any{}})
	return b
}

func TestRouteInference_SingleCandidateSelected(t *testing.T) {
	p := testProxy(NewDiscovery(), 11434)
	cands := []candidate{{id: "a", served: []string{"m"}}}
	reordered, out, pool := p.routeInference(cands, inferenceBody("m"))
	if out.SelectedID != "a" {
		t.Fatalf("selected = %q, want a", out.SelectedID)
	}
	if pool == nil {
		t.Fatal("pool should be reserved for the selected candidate")
	}
	if len(reordered) != 1 || reordered[0].id != "a" {
		t.Fatalf("reordered = %v", candIDs(reordered))
	}
}

func TestRouteInference_CapabilityGate_TextRequest(t *testing.T) {
	p := testProxy(NewDiscovery(), 11434)
	// Endpoint b declares vision-only (text disabled).
	cands := []candidate{
		{id: "a", served: []string{"m"}, routing: &noderec.EngineRouting{Capabilities: &noderec.EngineCaps{Text: boolPtr(true)}}},
		{id: "b", served: []string{"m"}, routing: &noderec.EngineRouting{Capabilities: &noderec.EngineCaps{Text: boolPtr(false), Vision: boolPtr(true)}}},
	}
	_, out, _ := p.routeInference(cands, inferenceBody("m"))
	if out.SelectedID != "a" {
		t.Fatalf("selected = %q, want a (b is vision-only)", out.SelectedID)
	}
	if out.Reasons["b"] != "TEXT_REQUIRED" {
		t.Errorf("reason for b = %q, want TEXT_REQUIRED", out.Reasons["b"])
	}
}

func TestRouteInference_VisionRequest(t *testing.T) {
	p := testProxy(NewDiscovery(), 11434)
	// A request with an image in the messages.
	body, _ := json.Marshal(map[string]any{
		"model": "m",
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": "describe"}, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "http://x"}}},
			},
		},
	})
	cands := []candidate{
		{id: "a", served: []string{"m"}, routing: &noderec.EngineRouting{Capabilities: &noderec.EngineCaps{Text: boolPtr(true), Vision: boolPtr(false)}}},
		{id: "b", served: []string{"m"}, routing: &noderec.EngineRouting{Capabilities: &noderec.EngineCaps{Text: boolPtr(true), Vision: boolPtr(true)}}},
	}
	_, out, _ := p.routeInference(cands, body)
	if out.SelectedID != "b" {
		t.Fatalf("selected = %q, want b (a lacks vision)", out.SelectedID)
	}
}

func TestRouteInference_ModelAvailabilityGate(t *testing.T) {
	p := testProxy(NewDiscovery(), 11434)
	cands := []candidate{
		{id: "a", served: []string{"model-x"}},
		{id: "b", served: []string{"model-y"}},
	}
	_, out, _ := p.routeInference(cands, inferenceBody("model-y"))
	if out.SelectedID != "b" {
		t.Fatalf("selected = %q, want b (only b serves model-y)", out.SelectedID)
	}
	if out.Reasons["a"] == "" {
		t.Error("reason for a should be non-empty (model not available)")
	}
}

func TestRouteInference_NoEligibleCandidate(t *testing.T) {
	p := testProxy(NewDiscovery(), 11434)
	cands := []candidate{
		{id: "a", served: []string{"other-model"}},
	}
	reordered, out, pool := p.routeInference(cands, inferenceBody("model-x"))
	if out.SelectedID != "" {
		t.Fatalf("selected = %q, want empty (no eligible)", out.SelectedID)
	}
	if pool != nil {
		t.Fatal("pool should be nil when nothing is eligible")
	}
	if len(reordered) != 1 || reordered[0].id != "a" {
		t.Fatalf("reordered should preserve original order when nothing eligible: %v", candIDs(reordered))
	}
}

func TestRouteInference_HealthyGate(t *testing.T) {
	p := testProxy(NewDiscovery(), 11434)
	p.setEndpointState("b", endpointState{Healthy: false, Enabled: true, Draining: false})
	cands := []candidate{
		{id: "a", served: []string{"m"}},
		{id: "b", served: []string{"m"}},
	}
	_, out, _ := p.routeInference(cands, inferenceBody("m"))
	if out.SelectedID != "a" {
		t.Fatalf("selected = %q, want a (b is unhealthy)", out.SelectedID)
	}
}

func TestRouteInference_EnabledGate(t *testing.T) {
	p := testProxy(NewDiscovery(), 11434)
	p.setEndpointState("a", endpointState{Healthy: true, Enabled: false, Draining: false})
	cands := []candidate{
		{id: "a", served: []string{"m"}},
		{id: "b", served: []string{"m"}},
	}
	_, out, _ := p.routeInference(cands, inferenceBody("m"))
	if out.SelectedID != "b" {
		t.Fatalf("selected = %q, want b (a is disabled)", out.SelectedID)
	}
}

func TestRouteInference_DrainingGate(t *testing.T) {
	p := testProxy(NewDiscovery(), 11434)
	p.setEndpointState("a", endpointState{Healthy: true, Enabled: true, Draining: true})
	cands := []candidate{
		{id: "a", served: []string{"m"}},
		{id: "b", served: []string{"m"}},
	}
	_, out, _ := p.routeInference(cands, inferenceBody("m"))
	if out.SelectedID != "b" {
		t.Fatalf("selected = %q, want b (a is draining)", out.SelectedID)
	}
}

func TestRouteInference_RoutingEnabledNil(t *testing.T) {
	p := testProxy(NewDiscovery(), 11434)
	// Enabled is nil (omitted) — should default to enabled.
	cands := []candidate{
		{id: "a", served: []string{"m"}, routing: &noderec.EngineRouting{Enabled: nil, Capabilities: capText()}},
		{id: "b", served: []string{"m"}, routing: &noderec.EngineRouting{Enabled: boolPtr(false), Capabilities: capText()}},
	}
	_, out, _ := p.routeInference(cands, inferenceBody("m"))
	if out.SelectedID != "a" {
		t.Fatalf("selected = %q, want a (b is explicitly disabled)", out.SelectedID)
	}
}

func TestRouteInference_CapacityExhaustion(t *testing.T) {
	p := testProxy(NewDiscovery(), 11434)
	// Endpoint a has capacity 1, endpoint b is unlimited.
	cands := []candidate{
		{id: "a", served: []string{"m"}, routing: &noderec.EngineRouting{StaticCapacity: 1, Priority: 0, Capabilities: capText()}},
		{id: "b", served: []string{"m"}},
	}
	// First request should select a (lower priority).
	_, out1, pool1 := p.routeInference(cands, inferenceBody("m"))
	if out1.SelectedID != "a" {
		t.Fatalf("first selected = %q, want a", out1.SelectedID)
	}
	// Second request should spill to b (a is full).
	_, out2, _ := p.routeInference(cands, inferenceBody("m"))
	if out2.SelectedID != "b" {
		t.Fatalf("second selected = %q, want b (a is full)", out2.SelectedID)
	}
	// Release a's reservation, third request should select a again.
	pool1.Release()
	_, out3, _ := p.routeInference(cands, inferenceBody("m"))
	if out3.SelectedID != "a" {
		t.Fatalf("third selected = %q, want a (released)", out3.SelectedID)
	}
}

func TestRouteInference_PriorityOrdering(t *testing.T) {
	p := testProxy(NewDiscovery(), 11434)
	cands := []candidate{
		{id: "low", served: []string{"m"}, routing: &noderec.EngineRouting{Priority: 100, Capabilities: capText()}},
		{id: "high", served: []string{"m"}, routing: &noderec.EngineRouting{Priority: 10, Capabilities: capText()}},
		{id: "mid", served: []string{"m"}, routing: &noderec.EngineRouting{Priority: 50, Capabilities: capText()}},
	}
	_, out, _ := p.routeInference(cands, inferenceBody("m"))
	if out.SelectedID != "high" {
		t.Fatalf("selected = %q, want high (lowest priority number)", out.SelectedID)
	}
}

func TestRouteInference_APIFamilyGate(t *testing.T) {
	p := testProxy(NewDiscovery(), 11434)
	cands := []candidate{
		{id: "a", served: []string{"m"}, routing: &noderec.EngineRouting{APIFamily: "openai", Capabilities: capText()}},
		{id: "b", served: []string{"m"}, routing: &noderec.EngineRouting{APIFamily: "ollama", Capabilities: capText()}},
	}
	// A request that doesn't declare a family should match any.
	_, out, _ := p.routeInference(cands, inferenceBody("m"))
	if out.SelectedID == "" {
		t.Fatal("no candidate selected for a family-agnostic request")
	}
}

func TestRouteInference_CapacityPoolPersistence(t *testing.T) {
	p := testProxy(NewDiscovery(), 11434)
	cands := []candidate{
		{id: "a", served: []string{"m"}, routing: &noderec.EngineRouting{StaticCapacity: 2, Capabilities: capText()}},
	}
	// Reserve 2 slots.
	_, out1, _ := p.routeInference(cands, inferenceBody("m"))
	_, out2, _ := p.routeInference(cands, inferenceBody("m"))
	if out1.SelectedID != "a" || out2.SelectedID != "a" {
		t.Fatalf("both should select a (capacity 2): %q, %q", out1.SelectedID, out2.SelectedID)
	}
	// Third request should find a full.
	_, out3, pool3 := p.routeInference(cands, inferenceBody("m"))
	if out3.SelectedID != "" {
		t.Fatalf("third should find a full, got %q", out3.SelectedID)
	}
	if pool3 != nil {
		t.Fatal("pool should be nil when a is full and nothing else eligible")
	}
}

func TestRouteInference_RewriteModelAlias(t *testing.T) {
	// Test the rewriteModelAlias helper directly.
	body := inferenceBody("local-coding")
	r := &noderec.EngineRouting{
		ModelRef: &noderec.EngineModelRef{
			PhysicalName: "actual-phys-model",
			Aliases:      []string{"local-coding"},
		},
	}
	out := rewriteModelAlias(body, r, "local-coding")
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var model string
	if err := json.Unmarshal(decoded["model"], &model); err != nil || model != "actual-phys-model" {
		t.Fatalf("model = %q, want actual-phys-model", model)
	}
}

func TestRouteInference_RewriteModelAlias_NoMatch(t *testing.T) {
	body := inferenceBody("some-other-model")
	r := &noderec.EngineRouting{
		ModelRef: &noderec.EngineModelRef{
			PhysicalName: "actual-phys-model",
			Aliases:      []string{"local-coding"},
		},
	}
	out := rewriteModelAlias(body, r, "some-other-model")
	if string(out) != string(body) {
		t.Fatalf("body should be unchanged for non-alias model")
	}
}

func TestRouteInference_RewriteModelAlias_NilRouting(t *testing.T) {
	body := inferenceBody("m")
	out := rewriteModelAlias(body, nil, "m")
	if string(out) != string(body) {
		t.Fatal("body should be unchanged for nil routing")
	}
}

func TestRouteInference_LegacyNilRouting(t *testing.T) {
	p := testProxy(NewDiscovery(), 11434)
	// Legacy: no routing metadata at all.
	cands := []candidate{
		{id: "a", served: []string{"m"}},
		{id: "b", served: []string{"m"}},
	}
	reordered, out, pool := p.routeInference(cands, inferenceBody("m"))
	if out.SelectedID == "" {
		t.Fatal("legacy candidates should be eligible")
	}
	if pool == nil {
		t.Fatal("pool should be reserved")
	}
	if len(reordered) != 2 {
		t.Fatalf("reordered should have 2 candidates, got %d", len(reordered))
	}
}

func candIDs(cands []candidate) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.id)
	}
	return out
}
