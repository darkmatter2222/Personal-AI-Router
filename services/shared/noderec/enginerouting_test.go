// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package noderec

import (
	"encoding/json"
	"strings"
	"testing"
)

// EngineRouting wire-format tests: serialization round-trips, the optional
// Enabled semantics (nil = enabled by convention), strategy values, per-model
// declarations, and the credentials-don't-cross-the-wire guarantee.

func b(p bool) *bool { return &p }

func TestEngineRoutingRoundTrip(t *testing.T) {
	text, vision, tools, streaming, reasoning := true, true, false, true, true
	in := &EngineRouting{
		APIFamily:          "openai",
		ContextMaxTokens:   262144,
		Priority:           10,
		StaticCapacity:     4,
		Pool:               "pool-a",
		Strategy:           "deterministic",
		Draining:           true,
		Enabled:            b(true),
		AuthHeadersPresent: true,
		Capabilities: &EngineCaps{
			Text:      &text,
			Vision:    &vision,
			Tools:     &tools,
			Streaming: &streaming,
			Reasoning: &reasoning,
		},
		ModelRef: &EngineModelRef{
			PhysicalName:     "actual-upstream-model",
			Aliases:          []string{"local-coding"},
			Caps:             &EngineCaps{Text: &text},
			ContextMaxTokens: 131072,
		},
		Models: []EngineModelRef{
			{PhysicalName: "model-a", Aliases: []string{"a1"}, Caps: &EngineCaps{Vision: &vision}, ContextMaxTokens: 32768},
			{PhysicalName: "model-b", Aliases: []string{"b1"}},
		},
		Timeouts: &EngineTimeouts{ConnectMS: 250, ResponseHeaderMS: 30000, FirstByteMS: 60000},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out EngineRouting
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.APIFamily != "openai" || out.ContextMaxTokens != 262144 || out.Priority != 10 ||
		out.StaticCapacity != 4 || out.Pool != "pool-a" || out.Strategy != "deterministic" ||
		!out.Draining || out.Enabled == nil || !*out.Enabled || !out.AuthHeadersPresent {
		t.Fatalf("scalar round-trip: %+v", out)
	}
	if out.Capabilities == nil || out.Capabilities.Text == nil || !*out.Capabilities.Text ||
		out.Capabilities.Vision == nil || !*out.Capabilities.Vision ||
		out.Capabilities.Tools == nil || *out.Capabilities.Tools ||
		out.Capabilities.Streaming == nil || !*out.Capabilities.Streaming ||
		out.Capabilities.Reasoning == nil || !*out.Capabilities.Reasoning {
		t.Fatalf("caps round-trip: %+v", out.Capabilities)
	}
	if out.ModelRef == nil || out.ModelRef.PhysicalName != "actual-upstream-model" ||
		len(out.ModelRef.Aliases) != 1 || out.ModelRef.Aliases[0] != "local-coding" ||
		out.ModelRef.Caps == nil || out.ModelRef.Caps.Text == nil ||
		out.ModelRef.ContextMaxTokens != 131072 {
		t.Fatalf("model ref round-trip: %+v", out.ModelRef)
	}
	if len(out.Models) != 2 || out.Models[0].PhysicalName != "model-a" ||
		out.Models[0].Caps == nil || out.Models[0].ContextMaxTokens != 32768 ||
		out.Models[1].PhysicalName != "model-b" {
		t.Fatalf("models round-trip: %+v", out.Models)
	}
	if out.Timeouts == nil || out.Timeouts.ConnectMS != 250 || out.Timeouts.ResponseHeaderMS != 30000 || out.Timeouts.FirstByteMS != 60000 {
		t.Fatalf("timeouts round-trip: %+v", out.Timeouts)
	}
}

func TestEngineRoutingEnabledOmittedRoundTripsToNil(t *testing.T) {
	// Omitted Enabled must come back as nil (the "enabled by convention"
	// default), never as a false pointer — the zero-value-inversion trap.
	data := []byte(`{}`)
	var out EngineRouting
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Enabled != nil {
		t.Fatalf("omitted enabled = %v, want nil", *out.Enabled)
	}
	// And explicit false survives as a false pointer.
	var out2 EngineRouting
	if err := json.Unmarshal([]byte(`{"enabled":false}`), &out2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out2.Enabled == nil || *out2.Enabled != false {
		t.Fatalf("explicit false = %v", out2.Enabled)
	}
	// And explicit true survives.
	var out3 EngineRouting
	if err := json.Unmarshal([]byte(`{"enabled":true}`), &out3); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out3.Enabled == nil || *out3.Enabled != true {
		t.Fatalf("explicit true = %v", out3.Enabled)
	}
}

func TestEngineRoutingOmittedFieldsSerializeAway(t *testing.T) {
	in := &EngineRouting{}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, key := range []string{
		`"api_family"`, `"capabilities"`, `"model_ref"`, `"context_max_tokens"`,
		`"priority"`, `"static_capacity"`, `"pool"`, `"timeouts"`,
		`"auth_headers_present"`, `"enabled"`, `"draining"`, `"strategy"`, `"models"`,
	} {
		if strings.Contains(s, key) {
			t.Errorf("zero-value routing serialized %s: %s", key, s)
		}
	}
}

func TestEngineRoutingUnknownEngineKeysSurvive(t *testing.T) {
	// The wire map is keyed by engine name: arbitrary engine IDs round-trip
	// with no source-code enum.
	node := DirectoryNode{
		HostUUID: "host-1",
		RoutingByEngine: map[string]*EngineRouting{
			"engine-test-random-98234": {APIFamily: "openai", Priority: 5},
			"custom-openai-runtime":    {StaticCapacity: 2},
			"my-vlm":                   {Capabilities: &EngineCaps{Vision: b(true)}},
		},
	}
	data, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out DirectoryNode
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.RoutingByEngine) != 3 {
		t.Fatalf("routing map = %d entries, want 3", len(out.RoutingByEngine))
	}
	if out.RoutingByEngine["engine-test-random-98234"] == nil ||
		out.RoutingByEngine["engine-test-random-98234"].Priority != 5 {
		t.Fatal("unknown engine entry lost")
	}
	if out.RoutingByEngine["my-vlm"].Capabilities == nil || out.RoutingByEngine["my-vlm"].Capabilities.Vision == nil {
		t.Fatal("my-vlm capabilities lost")
	}
}

func TestEngineRoutingNilVsEmptyMap(t *testing.T) {
	// A node with no routing state must serialize without the key at all
	// (omitempty), and an empty map must not silently become nil on read.
	node := DirectoryNode{HostUUID: "h"}
	data, _ := json.Marshal(node)
	if strings.Contains(string(data), "routingByEngine") {
		t.Fatalf("nil routing serialized: %s", data)
	}
	var out DirectoryNode
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.RoutingByEngine != nil {
		t.Fatalf("nil routing read back as %v", out.RoutingByEngine)
	}
}

func TestEngineRoutingCredentialsNeverSerialized(t *testing.T) {
	// The wire carries AuthHeadersPresent (a bool), never the header values:
	// a credential on a node's manifest must not cross the peer trust boundary.
	r := &EngineRouting{AuthHeadersPresent: true}
	data, _ := json.Marshal(r)
	s := string(data)
	if !strings.Contains(s, `"auth_headers_present":true`) {
		t.Fatalf("presence flag missing: %s", s)
	}
	for _, secret := range []string{"Bearer", "secret", "token", "key-123"} {
		if strings.Contains(s, secret) {
			t.Errorf("credential material %q leaked into wire format: %s", secret, s)
		}
	}
}

func TestEngineRoutingStrategyValues(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"scheduler", "scheduler"},
		{"deterministic", "deterministic"},
		{"", ""},
	}
	for _, tc := range cases {
		var r EngineRouting
		if err := json.Unmarshal([]byte(`{"strategy":"`+tc.in+`"}`), &r); err != nil {
			t.Fatalf("unmarshal %q: %v", tc.in, err)
		}
		if r.Strategy != tc.want {
			t.Errorf("strategy %q = %q", tc.in, r.Strategy)
		}
	}
}

func TestEngineModelRefRoundTrip(t *testing.T) {
	vision := true
	in := EngineModelRef{
		PhysicalName:     "phys-model",
		Aliases:          []string{"alias-1", "alias-2"},
		Caps:             &EngineCaps{Vision: &vision},
		ContextMaxTokens: 131072,
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out EngineModelRef
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.PhysicalName != "phys-model" || len(out.Aliases) != 2 ||
		out.Caps == nil || out.Caps.Vision == nil || !*out.Caps.Vision ||
		out.ContextMaxTokens != 131072 {
		t.Fatalf("round-trip: %+v", out)
	}
}

func TestEngineTimeoutsRoundTrip(t *testing.T) {
	in := EngineTimeouts{ConnectMS: 100, ResponseHeaderMS: 200, FirstByteMS: 300}
	data, _ := json.Marshal(in)
	var out EngineTimeouts
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("round-trip: %+v != %+v", out, in)
	}
}
