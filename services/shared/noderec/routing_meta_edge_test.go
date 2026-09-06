// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package noderec

import (
	"encoding/json"
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
