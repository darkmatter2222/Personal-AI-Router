// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEngineRouting_NeverSerializesAuth proves the routing/discovery contract
// (EngineRouting) structurally cannot carry a credential: LocalAuth is a
// separate, node-local type that EngineRouting does not embed or reference, so a
// fully-populated EngineRouting's JSON contains no auth material. This is the
// wire boundary that keeps a backend secret from crossing into discovery, the
// scheduler, or a peer.
func TestEngineRouting_NeverSerializesAuth(t *testing.T) {
	er := EngineRouting{
		APIFamily: APIFamilyOpenAI,
		Lifecycle: LifecycleExternal,
		Strategy:  StrategyDeterministicPriority,
		Priority:  intp(10),
		Capacity:  2,
		Draining:  true,
		Timeouts:  Timeouts{ConnectMS: 250, ResponseHeaderMS: 30000, FirstByteMS: 30000, ActionMS: 600000},
		Health:    Health{Path: "/health"},
		Models: []ModelRouting{{
			Physical:     "actual-upstream-model",
			Aliases:      []string{"local-coding"},
			Capabilities: Capabilities{Text: true, Tools: true},
			Context:      Context{MaxTokens: 262144},
		}},
	}
	b, err := json.Marshal(er)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := strings.ToLower(string(b))
	for _, bad := range []string{"auth", "authorization", "bearer", "token", "secret", "valueenv", "credential"} {
		if strings.Contains(js, bad) {
			t.Fatalf("EngineRouting JSON must not contain %q; got %s", bad, string(b))
		}
	}
}

// TestLocalAuth_IsSeparateFromEngineRouting documents that LocalAuth is only ever
// applied locally (ApplyAuth to an outbound header) and is never a field of the
// wire contract. If someone later added an auth field to EngineRouting this test
// would still pass, so the guard above (marshalling) is the real check; this
// test asserts the intended usage compiles and stays local.
func TestLocalAuth_IsSeparateFromEngineRouting(t *testing.T) {
	// A populated LocalAuth marshals with the secret only when serialized on its
	// own (node-local config), never as part of routing metadata.
	a := LocalAuth{Headers: []HeaderSpec{{Name: "Authorization", ValueEnv: "PAIR_TEST_BACKEND_TOKEN"}}}
	if a.Empty() {
		t.Fatal("populated LocalAuth should not be empty")
	}
	// The env NAME may appear in local config, but never the resolved value, and
	// never in EngineRouting. This is a compile-time separation: EngineRouting has
	// no LocalAuth field (proven by TestEngineRouting_NeverSerializesAuth).
}
