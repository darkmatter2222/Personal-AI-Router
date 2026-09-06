// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"encoding/json"
	"testing"
)

func ep(id string, models ...ModelRouting) Endpoint {
	return Endpoint{ID: id, Engine: "e", Healthy: true, Models: models}
}

func txtModel(physical string, aliases ...string) ModelRouting {
	return ModelRouting{Physical: physical, Aliases: aliases, Capabilities: Capabilities{Text: true}}
}

func TestMatchModel_Physical(t *testing.T) {
	e := ep("n1", txtModel("qwen-fast"))
	m, ok := MatchModel(e, "qwen-fast")
	if !ok || m.Physical != "qwen-fast" {
		t.Fatalf("physical match failed: %+v ok=%v", m, ok)
	}
}

func TestMatchModel_Alias(t *testing.T) {
	e := ep("n1", txtModel("qwen-fast", "local-coding", "coder"))
	m, ok := MatchModel(e, "local-coding")
	if !ok || m.Physical != "qwen-fast" {
		t.Fatalf("alias should resolve to physical qwen-fast, got %+v ok=%v", m, ok)
	}
	m, ok = MatchModel(e, "coder")
	if !ok || m.Physical != "qwen-fast" {
		t.Fatalf("second alias failed: %+v ok=%v", m, ok)
	}
}

func TestMatchModel_PhysicalPrecedenceOverAlias(t *testing.T) {
	// "shared" is model B's physical name and model A's alias. A request for
	// "shared" must resolve to B (physical precedence), deterministically.
	e := ep("n1",
		ModelRouting{Physical: "modelA", Aliases: []string{"shared"}, Capabilities: Capabilities{Text: true}},
		ModelRouting{Physical: "shared", Capabilities: Capabilities{Text: true}},
	)
	m, ok := MatchModel(e, "shared")
	if !ok || m.Physical != "shared" {
		t.Fatalf("physical precedence failed: got %+v", m)
	}
}

func TestMatchModel_NotFoundAndEmpty(t *testing.T) {
	e := ep("n1", txtModel("qwen-fast", "local-coding"))
	if _, ok := MatchModel(e, "gpt-4"); ok {
		t.Fatal("unknown model must not match")
	}
	if _, ok := MatchModel(e, ""); ok {
		t.Fatal("empty requested must not match")
	}
	if _, ok := MatchModel(ep("empty"), "anything"); ok {
		t.Fatal("endpoint with no models must not match")
	}
}

func TestMatchModel_DifferentPhysicalAcrossEndpoints(t *testing.T) {
	// The same logical alias maps to different physical models on different
	// endpoints (the core aliasing use case).
	a := ep("A", txtModel("qwen-fast", "local-coding"))
	b := ep("B", txtModel("qwen-q4", "local-coding"))
	c := ep("C", txtModel("flash-next", "local-coding"))
	if p, _ := PhysicalFor(a, "local-coding"); p != "qwen-fast" {
		t.Fatalf("A physical = %q", p)
	}
	if p, _ := PhysicalFor(b, "local-coding"); p != "qwen-q4" {
		t.Fatalf("B physical = %q", p)
	}
	if p, _ := PhysicalFor(c, "local-coding"); p != "flash-next" {
		t.Fatalf("C physical = %q", p)
	}
}

func TestOwnsAndPhysicalFor(t *testing.T) {
	e := ep("n1", txtModel("qwen-fast", "local-coding"))
	if !Owns(e, "qwen-fast") || !Owns(e, "local-coding") {
		t.Fatal("Owns should be true for physical and alias")
	}
	if Owns(e, "nope") {
		t.Fatal("Owns should be false for unknown")
	}
	if p, ok := PhysicalFor(e, "qwen-fast"); !ok || p != "qwen-fast" {
		t.Fatalf("PhysicalFor physical = %q ok=%v", p, ok)
	}
	if _, ok := PhysicalFor(e, "nope"); ok {
		t.Fatal("PhysicalFor unknown must be false")
	}
}

func TestRewriteModel(t *testing.T) {
	// Alias -> physical, other fields preserved (including a number and nested).
	body := []byte(`{"model":"local-coding","temperature":0.7,"messages":[{"role":"user","content":"hi"}],"max_tokens":128}`)
	out, changed := RewriteModel(body, "qwen-fast")
	if !changed {
		t.Fatal("expected rewrite")
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("rewritten body invalid json: %v", err)
	}
	var model string
	_ = json.Unmarshal(got["model"], &model)
	if model != "qwen-fast" {
		t.Fatalf("model = %q, want qwen-fast", model)
	}
	if string(got["temperature"]) != "0.7" {
		t.Fatalf("temperature not preserved exactly: %s", got["temperature"])
	}
	if string(got["max_tokens"]) != "128" {
		t.Fatalf("max_tokens not preserved: %s", got["max_tokens"])
	}
	if len(got["messages"]) == 0 {
		t.Fatal("messages dropped")
	}
}

func TestRewriteModel_NoChangeCases(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		physical string
	}{
		{"already physical", `{"model":"qwen-fast","x":1}`, "qwen-fast"},
		{"no model field", `{"messages":[]}`, "qwen-fast"},
		{"not an object", `[1,2,3]`, "qwen-fast"},
		{"malformed", `{bad`, "qwen-fast"},
		{"empty physical", `{"model":"x"}`, ""},
		{"empty body", ``, "qwen-fast"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, changed := RewriteModel([]byte(c.body), c.physical)
			if changed {
				t.Fatalf("expected no change, got changed=true out=%s", out)
			}
			if string(out) != c.body {
				t.Fatalf("body mutated on no-change: %q -> %q", c.body, out)
			}
		})
	}
}
