// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package noderec

import (
	"encoding/json"
	"strings"
	"testing"

	"nvpair-shared/routing"
)

// TestDirectoryNode_MultipleEnginesRoutingIndependent: several engines' routing
// metadata on one node round-trips independently, each keyed by its own engine
// id, with per-engine fields preserved.
func TestDirectoryNode_MultipleEnginesRoutingIndependent(t *testing.T) {
	in := DirectoryNode{
		HostUUID: "u",
		Services: map[ServiceKey]ServiceStatus{},
		RoutingByEngine: map[string]routing.EngineRouting{
			"engine-a": {APIFamily: routing.APIFamilyOpenAI, Priority: prio(5)},
			"engine-b": {APIFamily: routing.APIFamilyOllama, Priority: prio(20), Capacity: 3},
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
	a, oka := out.EngineRouting("engine-a")
	bb, okb := out.EngineRouting("engine-b")
	if !oka || !okb {
		t.Fatalf("both engines should round-trip: a=%v b=%v", oka, okb)
	}
	if a.ResolvedPriority() != 5 || a.APIFamily != routing.APIFamilyOpenAI {
		t.Fatalf("engine-a fields lost: %+v", a)
	}
	if bb.ResolvedPriority() != 20 || bb.APIFamily != routing.APIFamilyOllama || bb.Capacity != 3 {
		t.Fatalf("engine-b fields lost: %+v", bb)
	}
	// An engine not present returns the legacy "use existing routing" signal.
	if _, ok := out.EngineRouting("engine-c"); ok {
		t.Fatal("absent engine must return ok=false")
	}
}

// TestDirectoryNode_RoutingByEngineNeverCarriesAuth: routing metadata on the
// discovery wire structurally cannot carry a backend credential (routing.
// EngineRouting has no auth field), so a peer never receives another node's
// secret.
func TestDirectoryNode_RoutingByEngineNeverCarriesAuth(t *testing.T) {
	n := DirectoryNode{
		HostUUID: "u",
		Services: map[ServiceKey]ServiceStatus{},
		RoutingByEngine: map[string]routing.EngineRouting{
			"custom-runtime": {
				APIFamily: routing.APIFamilyOpenAI,
				Priority:  prio(10),
				Models: []routing.ModelRouting{{
					Physical: "actual-upstream-model", Aliases: []string{"local-coding"},
					Capabilities: routing.Capabilities{Text: true},
				}},
			},
		},
	}
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := strings.ToLower(string(b))
	for _, bad := range []string{"authorization", "bearer", "secret", "valueenv", "credential"} {
		if strings.Contains(js, bad) {
			t.Fatalf("discovery routing metadata leaked %q: %s", bad, string(b))
		}
	}
}
