// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"strings"
	"testing"
)

// openAIChat builds an OpenAI chat-completions body. The base closes the
// message and messages array; a mutate callback then appends top-level
// fields (stream, tools, max_tokens) as OpenAI places them at the request
// root, and the final "}" closes the outer object.
func openAIChat(t *testing.T, mutate func(b *strings.Builder)) []byte {
	t.Helper()
	b := &strings.Builder{}
	b.WriteString(`{"model":"local-coding","messages":[{"role":"user","content":"hi"}]`)
	if mutate != nil {
		mutate(b)
	}
	b.WriteString("}")
	return []byte(b.String())
}

func TestClassifyDetectsVisionToolsStreaming(t *testing.T) {
	body := openAIChat(t, func(b *strings.Builder) {
		b.WriteString(`,"stream":true,"tools":[{"type":"function","function":{"name":"f"}}]`)
	})
	req := Classify(body)
	if !req.RequiresStreaming {
		t.Error("expected streaming required")
	}
	if !req.RequiresTools {
		t.Error("expected tools required")
	}

	// Vision: an image_url block in a message content array. Built by
	// concatenation so the closing symbols (content ], message }, messages ],
	// outer }) are explicit and the JSON is guaranteed well-formed.
	vbody := []byte(
		`{"model":"m","messages":[` +
			`{"role":"user","content":[` +
			`{"type":"text","text":"describe"},` +
			`{"type":"image_url","image_url":{"url":"http://example.com/photo.jpg"}}` +
			`]}` +
			`]}`)
	vreq := Classify(vbody)
	if !vreq.HasImages {
		t.Error("expected image detection")
	}
	if vreq.RequiredContext <= 0 {
		t.Error("expected positive required context")
	}
}

func TestCapsGates(t *testing.T) {
	caps := EndpointCaps{
		Vision: boolPtr(true), Tools: boolPtr(true), Streaming: boolPtr(true),
		MaxContext: 262144,
	}
	req := Req{HasImages: true, RequiresTools: true, RequiresStreaming: true, RequiredContext: 1000}
	if reason := caps.Check(req); reason != "" {
		t.Errorf("eligible endpoint wrongly rejected: %s", reason)
	}

	// Vision-only endpoint must reject an image request.
	noVision := EndpointCaps{Text: boolPtr(true), Streaming: boolPtr(true), MaxContext: 262144}
	if reason := noVision.Check(req); reason != ReasonVisionRequired {
		t.Errorf("want VISION_REQUIRED, got %q", reason)
	}

	// Small-context endpoint must reject a large-context request.
	small := EndpointCaps{Text: boolPtr(true), Streaming: boolPtr(true), Tools: boolPtr(true), Vision: boolPtr(true), MaxContext: 65536}
	bigReq := Req{HasImages: true, RequiresTools: true, RequiresStreaming: true, RequiredContext: 200000}
	if reason := small.Check(bigReq); reason != ReasonContextTooSmall {
		t.Errorf("want CONTEXT_TOO_SMALL, got %q", reason)
	}

	// An undeclared (nil) capability is unsupported.
	undecl := EndpointCaps{Text: boolPtr(true)}
	imgReq := Req{HasImages: true}
	if reason := undecl.Check(imgReq); reason != ReasonVisionRequired {
		t.Errorf("undeclared vision should reject image request, got %q", reason)
	}
}

func TestSelectDeterministicPriorityAndSpillover(t *testing.T) {
	a := Candidate{ID: "A", Priority: 10, Capacity: NewPool(2), Healthy: true, Enabled: true,
		Caps: EndpointCaps{Text: boolPtr(true), Tools: boolPtr(true), Streaming: boolPtr(true), MaxContext: 262144}}
	b := Candidate{ID: "B", Priority: 20, Capacity: NewPool(1), Healthy: true, Enabled: true,
		Caps: EndpointCaps{Text: boolPtr(true), Tools: boolPtr(true), Streaming: boolPtr(true), MaxContext: 262144}}
	c := Candidate{ID: "C", Priority: 30, Capacity: NewPool(3), Healthy: true, Enabled: true,
		Caps: EndpointCaps{Text: boolPtr(true), Tools: boolPtr(true), Streaming: boolPtr(true), Vision: boolPtr(true), MaxContext: 262144}}
	e := Candidate{ID: "E", Priority: 5, Capacity: NewPool(10), Healthy: true, Enabled: true,
		Caps: EndpointCaps{Text: boolPtr(true), Streaming: boolPtr(true), MaxContext: 65536}}

	// A normal coding request requires tools. Mock E (priority 5) is
	// capability-ineligible (tools=false), so despite its best priority and
	// zero load it must be rejected, and A (priority 10) is selected before
	// B (20) and C (30).
	req := Req{RequiresTools: true}
	out := Select([]Candidate{a, b, c, e}, req)
	if out.SelectedID != "A" {
		t.Fatalf("selected %q, want A (best eligible priority)", out.SelectedID)
	}
	if out.Reasons["E"] != ReasonToolsRequired {
		t.Errorf("E reason = %q, want TOOLS_REQUIRED", out.Reasons["E"])
	}

	// 2) Fill A's two slots; next request spills to B.
	a.Capacity.Reserve()
	a.Capacity.Reserve()
	out = Select([]Candidate{a, b, c, e}, req)
	if out.SelectedID != "B" {
		t.Fatalf("after A full, selected %q, want B", out.SelectedID)
	}
	if out.Reasons["A"] != ReasonCapacityFull {
		t.Errorf("A reason = %q, want CAPACITY_FULL", out.Reasons["A"])
	}

	// 3) Fill B; next spills to C.
	b.Capacity.Reserve()
	out = Select([]Candidate{a, b, c, e}, req)
	if out.SelectedID != "C" {
		t.Fatalf("after B full, selected %q, want C", out.SelectedID)
	}

	// 4) Release A's capacity; A wins again (deterministic recovery).
	a.Capacity.Release()
	a.Capacity.Release()
	out = Select([]Candidate{a, b, c, e}, req)
	if out.SelectedID != "A" {
		t.Fatalf("recovered, selected %q, want A", out.SelectedID)
	}
}

func TestPoolConcurrencySafe(t *testing.T) {
	pool := NewPool(2)
	const n = 50
	done := make(chan bool, n)
	for i := 0; i < n; i++ {
		go func() { done <- pool.Reserve() }()
	}
	admitted := 0
	for i := 0; i < n; i++ {
		if <-done {
			admitted++
		}
	}
	if admitted != 2 {
		t.Fatalf("admitted %d, want 2", admitted)
	}
	if pool.Active() != 2 {
		t.Fatalf("active = %d, want 2", pool.Active())
	}
	for i := 0; i < 2; i++ {
		pool.Release()
	}
	if pool.Active() != 0 {
		t.Fatalf("active after release = %d, want 0", pool.Active())
	}
	// After full release, all reservations can be re-admitted.
	pool.Reserve()
	pool.Release()
}

func TestSelectDisabledDrainingUnhealthy(t *testing.T) {
	cands := []Candidate{
		{ID: "disabled", Priority: 1, Enabled: false, Healthy: true, Capacity: NewPool(5),
			Caps: EndpointCaps{Text: boolPtr(true)}},
		{ID: "draining", Priority: 2, Enabled: true, Draining: true, Healthy: true, Capacity: NewPool(5),
			Caps: EndpointCaps{Text: boolPtr(true)}},
		{ID: "unhealthy", Priority: 3, Enabled: true, Healthy: false, Capacity: NewPool(5),
			Caps: EndpointCaps{Text: boolPtr(true)}},
		{ID: "ok", Priority: 4, Enabled: true, Healthy: true, Capacity: NewPool(5),
			Caps: EndpointCaps{Text: boolPtr(true)}},
	}
	out := Select(cands, Req{})
	if out.SelectedID != "ok" {
		t.Fatalf("selected %q, want ok", out.SelectedID)
	}
	if out.Reasons["disabled"] != ReasonEndpointDisabled ||
		out.Reasons["draining"] != ReasonEndpointDraining ||
		out.Reasons["unhealthy"] != ReasonEndpointUnhealthy {
		t.Errorf("reasons = %#v", out.Reasons)
	}
}

// boolPtr helper.
func boolPtr(b bool) *bool { return &b }
