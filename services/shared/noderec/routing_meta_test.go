// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package noderec

import (
	"encoding/json"
	"strings"
	"testing"

	"nvpair-shared/routing"
)

func prio(i int) *int { return &i }

// TestDirectoryNode_RoutingByEngineRoundTrip verifies that per-engine routing
// metadata for an arbitrary (non-built-in) engine id survives a full JSON
// round-trip on the discovery wire, keys preserved, with no source enum entry.
func TestDirectoryNode_RoutingByEngineRoundTrip(t *testing.T) {
	const custom = "engine-982341" // not a built-in id; exists in no enum
	in := DirectoryNode{
		HostUUID: "uuid-x",
		Services: map[ServiceKey]ServiceStatus{ServiceEngineManager: {Port: 14322}},
		ModelsByEngine: map[string][]string{
			custom: {"actual-upstream-model"},
		},
		RoutingByEngine: map[string]routing.EngineRouting{
			custom: {
				APIFamily: routing.APIFamilyOpenAI,
				Lifecycle: routing.LifecycleExternal,
				Strategy:  routing.StrategyDeterministicPriority,
				Priority:  prio(10),
				Capacity:  2,
				Draining:  true,
				Timeouts:  routing.Timeouts{ConnectMS: 250, FirstByteMS: 30000},
				Models: []routing.ModelRouting{{
					Physical:     "actual-upstream-model",
					Aliases:      []string{"local-coding"},
					Capabilities: routing.Capabilities{Text: true, Tools: true},
					Context:      routing.Context{MaxTokens: 262144},
				}},
			},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out DirectoryNode
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	er, ok := out.EngineRouting(custom)
	if !ok {
		t.Fatalf("routing metadata for %q lost in round-trip", custom)
	}
	if er.ResolvedPriority() != 10 || er.Capacity != 2 || !er.External() || !er.Draining {
		t.Fatalf("routing fields lost: %+v", er)
	}
	if len(er.Models) != 1 || er.Models[0].Physical != "actual-upstream-model" ||
		len(er.Models[0].Aliases) != 1 || er.Models[0].Aliases[0] != "local-coding" ||
		er.Models[0].Context.MaxTokens != 262144 {
		t.Fatalf("model routing lost: %+v", er.Models)
	}
}

func TestDirectoryNode_EngineRoutingLegacyDefault(t *testing.T) {
	// A node with no routing metadata (legacy/built-in peer) returns ok=false,
	// which callers read as "use existing inventory routing", not "route nowhere".
	var n DirectoryNode
	if _, ok := n.EngineRouting("ollama"); ok {
		t.Fatal("nil RoutingByEngine must return ok=false")
	}
	n.RoutingByEngine = map[string]routing.EngineRouting{"a": {}}
	if _, ok := n.EngineRouting("b"); ok {
		t.Fatal("unknown engine must return ok=false")
	}
	if _, ok := n.EngineRouting("a"); !ok {
		t.Fatal("declared engine must return ok=true")
	}
}

func TestDirectoryNode_RoutingByEngineOmittedWhenAbsent(t *testing.T) {
	// The field must be omitempty so a legacy node's wire form is unchanged.
	b, _ := json.Marshal(DirectoryNode{HostUUID: "u", Services: map[ServiceKey]ServiceStatus{}})
	if strings.Contains(string(b), "routingByEngine") {
		t.Fatalf("routingByEngine must be omitted when absent: %s", b)
	}
}
