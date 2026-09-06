// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"encoding/json"
	"testing"
	"time"
)

func intp(i int) *int { return &i }

func TestEnabledDefaultsToTrue(t *testing.T) {
	// The zero value must be ENABLED: an omitted Disabled field can never
	// silently turn an endpoint off.
	var zero EngineRouting
	if !zero.Enabled() {
		t.Fatal("zero-value EngineRouting must be enabled")
	}
	if (EngineRouting{Disabled: true}).Enabled() {
		t.Fatal("Disabled:true must not be enabled")
	}
}

func TestResolvedPriorityOmittedIsLowest(t *testing.T) {
	var zero EngineRouting
	if got := zero.ResolvedPriority(); got != DefaultPriority {
		t.Fatalf("omitted priority = %d, want DefaultPriority %d", got, DefaultPriority)
	}
	// An explicit 0 must remain 0 (highest preference), distinct from omitted.
	if got := (EngineRouting{Priority: intp(0)}).ResolvedPriority(); got != 0 {
		t.Fatalf("explicit priority 0 = %d, want 0", got)
	}
	if got := (EngineRouting{Priority: intp(10)}).ResolvedPriority(); got != 10 {
		t.Fatalf("priority = %d, want 10", got)
	}
	if DefaultPriority <= 10 {
		t.Fatal("DefaultPriority must sort after any realistic explicit priority")
	}
}

func TestResolvedStrategyDefault(t *testing.T) {
	if got := (EngineRouting{}).ResolvedStrategy(); got != StrategyDefault {
		t.Fatalf("default strategy = %q, want default", got)
	}
	if got := (EngineRouting{Strategy: StrategyDeterministicPriority}).ResolvedStrategy(); got != StrategyDeterministicPriority {
		t.Fatalf("strategy = %q", got)
	}
}

func TestExternalLifecycle(t *testing.T) {
	if (EngineRouting{}).External() {
		t.Fatal("zero lifecycle (managed) must not be external")
	}
	if !(EngineRouting{Lifecycle: LifecycleExternal}).External() {
		t.Fatal("external lifecycle must report external")
	}
}

func TestTimeoutResolution(t *testing.T) {
	def := 5 * time.Second
	// Zero -> default; positive -> that many ms; each field independent.
	cases := []struct {
		name string
		to   Timeouts
		get  func(Timeouts) time.Duration
		want time.Duration
	}{
		{"connect omitted", Timeouts{}, func(t Timeouts) time.Duration { return t.Connect(def) }, def},
		{"connect set", Timeouts{ConnectMS: 250}, func(t Timeouts) time.Duration { return t.Connect(def) }, 250 * time.Millisecond},
		{"header omitted", Timeouts{}, func(t Timeouts) time.Duration { return t.ResponseHeader(def) }, def},
		{"header set", Timeouts{ResponseHeaderMS: 30000}, func(t Timeouts) time.Duration { return t.ResponseHeader(def) }, 30 * time.Second},
		{"firstbyte omitted", Timeouts{}, func(t Timeouts) time.Duration { return t.FirstByte(def) }, def},
		{"firstbyte set", Timeouts{FirstByteMS: 45000}, func(t Timeouts) time.Duration { return t.FirstByte(def) }, 45 * time.Second},
		{"action omitted", Timeouts{}, func(t Timeouts) time.Duration { return t.Action(def) }, def},
		{"action set", Timeouts{ActionMS: 600000}, func(t Timeouts) time.Duration { return t.Action(def) }, 600 * time.Second},
		{"negative -> default", Timeouts{ConnectMS: -1}, func(t Timeouts) time.Duration { return t.Connect(def) }, def},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.get(c.to); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestTimeoutsIndependent(t *testing.T) {
	// One endpoint's long first-byte timeout must not change another field's
	// resolution.
	to := Timeouts{FirstByteMS: 600000}
	if got := to.ResponseHeader(2 * time.Second); got != 2*time.Second {
		t.Fatalf("response-header should stay at default, got %v", got)
	}
	if got := to.FirstByte(2 * time.Second); got != 600*time.Second {
		t.Fatalf("first-byte should be its own value, got %v", got)
	}
}

func TestEndpointBuilderAppliesDefaults(t *testing.T) {
	er := EngineRouting{
		APIFamily: APIFamilyOpenAI,
		Strategy:  StrategyDeterministicPriority,
		Priority:  intp(10),
		Capacity:  4,
		Models:    []ModelRouting{{Physical: "m", Capabilities: Capabilities{Text: true}}},
	}
	ep := er.Endpoint("node-1", "custom-runtime", true)
	if ep.ID != "node-1" || ep.Engine != "custom-runtime" {
		t.Fatalf("identity wrong: %+v", ep)
	}
	if ep.APIFamily != APIFamilyOpenAI || ep.Strategy != StrategyDeterministicPriority {
		t.Fatalf("family/strategy wrong: %+v", ep)
	}
	if ep.Priority != 10 || ep.Capacity != 4 || !ep.Healthy {
		t.Fatalf("resolved fields wrong: %+v", ep)
	}
	if ep.Disabled || ep.Draining {
		t.Fatalf("state defaults wrong: %+v", ep)
	}

	// Omitted priority resolves to DefaultPriority through the builder too.
	ep2 := (EngineRouting{}).Endpoint("n", "e", false)
	if ep2.Priority != DefaultPriority {
		t.Fatalf("builder omitted priority = %d", ep2.Priority)
	}
	if ep2.Healthy {
		t.Fatal("builder must carry the healthy=false argument")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	orig := EngineRouting{
		APIFamily: APIFamilyOpenAI,
		Lifecycle: LifecycleExternal,
		Strategy:  StrategyDeterministicPriority,
		Priority:  intp(10),
		Capacity:  2,
		Disabled:  false,
		Draining:  true,
		Timeouts:  Timeouts{ConnectMS: 250, ResponseHeaderMS: 30000, FirstByteMS: 30000},
		Health:    Health{Path: "/health"},
		Models: []ModelRouting{{
			Physical:     "actual-upstream-model",
			Aliases:      []string{"local-coding"},
			Capabilities: Capabilities{Text: true, Tools: true},
			Context:      Context{MaxTokens: 262144},
		}},
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back EngineRouting
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ResolvedPriority() != 10 || back.Capacity != 2 || !back.External() || back.Draining != true {
		t.Fatalf("round-trip lost fields: %+v", back)
	}
	if len(back.Models) != 1 || back.Models[0].Physical != "actual-upstream-model" ||
		len(back.Models[0].Aliases) != 1 || back.Models[0].Aliases[0] != "local-coding" {
		t.Fatalf("round-trip lost model: %+v", back.Models)
	}
	if back.Models[0].Context.MaxTokens != 262144 {
		t.Fatalf("round-trip lost context: %+v", back.Models[0].Context)
	}
}

func TestDisabledOmittedWhenFalse(t *testing.T) {
	// A false Disabled (the safe default) must serialise as absent, so a legacy
	// consumer that never sets it reads the same enabled default.
	b, _ := json.Marshal(EngineRouting{})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, present := m["disabled"]; present {
		t.Fatalf("disabled must be omitempty when false, got %s", b)
	}
	if _, present := m["priority"]; present {
		t.Fatalf("nil priority must be omitted, got %s", b)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		er      EngineRouting
		wantErr bool
	}{
		{"empty ok", EngineRouting{}, false},
		{"full ok", EngineRouting{
			APIFamily: APIFamilyOpenAI, Lifecycle: LifecycleExternal, Strategy: StrategyDeterministicPriority,
			Priority: intp(0), Capacity: 3, Timeouts: Timeouts{ConnectMS: 1},
			Models: []ModelRouting{{Physical: "a", Aliases: []string{"x"}}},
		}, false},
		{"bad family", EngineRouting{APIFamily: "grpc"}, true},
		{"bad lifecycle", EngineRouting{Lifecycle: "adopt"}, true},
		{"bad strategy", EngineRouting{Strategy: "round-robin"}, true},
		{"negative capacity", EngineRouting{Capacity: -1}, true},
		{"negative timeout", EngineRouting{Timeouts: Timeouts{FirstByteMS: -5}}, true},
		{"empty physical", EngineRouting{Models: []ModelRouting{{Physical: ""}}}, true},
		{"newline physical", EngineRouting{Models: []ModelRouting{{Physical: "a\nb"}}}, true},
		{"newline alias", EngineRouting{Models: []ModelRouting{{Physical: "a", Aliases: []string{"x\r"}}}}, true},
		{"dup physical", EngineRouting{Models: []ModelRouting{{Physical: "a"}, {Physical: "a"}}}, true},
		{"slash colon ok", EngineRouting{Models: []ModelRouting{{Physical: "repo/qwen-fast:latest"}}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.er.Validate()
			if (err != nil) != c.wantErr {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, c.wantErr)
			}
		})
	}
}
