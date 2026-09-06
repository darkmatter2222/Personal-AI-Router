// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"nvpair-shared/noderec"
)

// RoutingByEngine round-trip tests: the per-engine routing metadata survives
// the node-scanner's equality check and apply path without loss.

func boolPtr(b bool) *bool { return &b }

func TestSameRouting_NilNil(t *testing.T) {
	if !sameRouting(nil, nil) {
		t.Fatal("nil == nil should be true")
	}
}

func TestSameRouting_EmptyEmpty(t *testing.T) {
	if !sameRouting(map[string]*noderec.EngineRouting{}, map[string]*noderec.EngineRouting{}) {
		t.Fatal("empty == empty should be true")
	}
}

func TestSameRouting_NilEmpty(t *testing.T) {
	if !sameRouting(nil, map[string]*noderec.EngineRouting{}) {
		t.Fatal("nil == empty should be true")
	}
	if !sameRouting(map[string]*noderec.EngineRouting{}, nil) {
		t.Fatal("empty == nil should be true")
	}
}

func TestSameRouting_Identical(t *testing.T) {
	a := map[string]*noderec.EngineRouting{
		"ollama": {
			APIFamily:      "openai",
			Priority:       10,
			StaticCapacity: 4,
			Capabilities:   &noderec.EngineCaps{Text: boolPtr(true), Vision: boolPtr(false)},
			ContextMaxTokens: 262144,
			AuthHeadersPresent: true,
		},
		"lmstudio": {
			APIFamily: "openai",
			Pool:      "pool-a",
		},
	}
	b := map[string]*noderec.EngineRouting{
		"ollama": {
			APIFamily:      "openai",
			Priority:       10,
			StaticCapacity: 4,
			Capabilities:   &noderec.EngineCaps{Text: boolPtr(true), Vision: boolPtr(false)},
			ContextMaxTokens: 262144,
			AuthHeadersPresent: true,
		},
		"lmstudio": {
			APIFamily: "openai",
			Pool:      "pool-a",
		},
	}
	if !sameRouting(a, b) {
		t.Fatal("identical maps should be equal")
	}
}

func TestSameRouting_DifferentPriority(t *testing.T) {
	a := map[string]*noderec.EngineRouting{"ollama": {Priority: 10}}
	b := map[string]*noderec.EngineRouting{"ollama": {Priority: 20}}
	if sameRouting(a, b) {
		t.Fatal("different priority should not be equal")
	}
}

func TestSameRouting_DifferentCaps(t *testing.T) {
	a := map[string]*noderec.EngineRouting{"ollama": {Capabilities: &noderec.EngineCaps{Text: boolPtr(true)}}}
	b := map[string]*noderec.EngineRouting{"ollama": {Capabilities: &noderec.EngineCaps{Text: boolPtr(false)}}}
	if sameRouting(a, b) {
		t.Fatal("different caps should not be equal")
	}
}

func TestSameRouting_DifferentEngine(t *testing.T) {
	a := map[string]*noderec.EngineRouting{"ollama": {}}
	b := map[string]*noderec.EngineRouting{"lmstudio": {}}
	if sameRouting(a, b) {
		t.Fatal("different engine keys should not be equal")
	}
}

func TestSameRouting_MissingKey(t *testing.T) {
	a := map[string]*noderec.EngineRouting{"ollama": {}, "lmstudio": {}}
	b := map[string]*noderec.EngineRouting{"ollama": {}}
	if sameRouting(a, b) {
		t.Fatal("missing key should not be equal")
	}
}

func TestSameRouting_NilValue(t *testing.T) {
	a := map[string]*noderec.EngineRouting{"ollama": nil}
	b := map[string]*noderec.EngineRouting{"ollama": nil}
	if !sameRouting(a, b) {
		t.Fatal("nil value == nil value should be true")
	}
	c := map[string]*noderec.EngineRouting{"ollama": {}}
	if sameRouting(a, c) {
		t.Fatal("nil value != present value should be false")
	}
}

func TestSameRouting_ModelRef(t *testing.T) {
	a := map[string]*noderec.EngineRouting{
		"ollama": {
			ModelRef: &noderec.EngineModelRef{
				PhysicalName: "actual-model",
				Aliases:      []string{"local-coding"},
			},
		},
	}
	b := map[string]*noderec.EngineRouting{
		"ollama": {
			ModelRef: &noderec.EngineModelRef{
				PhysicalName: "actual-model",
				Aliases:      []string{"local-coding"},
			},
		},
	}
	if !sameRouting(a, b) {
		t.Fatal("identical model refs should be equal")
	}
	b["ollama"].ModelRef.Aliases = []string{"different-alias"}
	if sameRouting(a, b) {
		t.Fatal("different aliases should not be equal")
	}
}

func TestSameRouting_Timeouts(t *testing.T) {
	a := map[string]*noderec.EngineRouting{
		"ollama": {Timeouts: &noderec.EngineTimeouts{ConnectMS: 250, FirstByteMS: 60000}},
	}
	b := map[string]*noderec.EngineRouting{
		"ollama": {Timeouts: &noderec.EngineTimeouts{ConnectMS: 250, FirstByteMS: 60000}},
	}
	if !sameRouting(a, b) {
		t.Fatal("identical timeouts should be equal")
	}
	b["ollama"].Timeouts.ConnectMS = 500
	if sameRouting(a, b) {
		t.Fatal("different timeout values should not be equal")
	}
}

func TestSameRouting_Strategy(t *testing.T) {
	a := map[string]*noderec.EngineRouting{"ollama": {Strategy: "deterministic", Priority: 5}}
	b := map[string]*noderec.EngineRouting{"ollama": {Strategy: "scheduler", Priority: 5}}
	if sameRouting(a, b) {
		t.Fatal("different strategy should not be equal")
	}
}

func TestSameRouting_EnabledPtr(t *testing.T) {
	a := map[string]*noderec.EngineRouting{"ollama": {Enabled: boolPtr(true)}}
	b := map[string]*noderec.EngineRouting{"ollama": {Enabled: boolPtr(true)}}
	if !sameRouting(a, b) {
		t.Fatal("identical enabled=true should be equal")
	}
	c := map[string]*noderec.EngineRouting{"ollama": {Enabled: boolPtr(false)}}
	if sameRouting(a, c) {
		t.Fatal("enabled=true != enabled=false")
	}
	d := map[string]*noderec.EngineRouting{"ollama": {Enabled: nil}}
	if sameRouting(a, d) {
		t.Fatal("enabled=true != enabled=nil")
	}
}

func TestSameRouting_Draining(t *testing.T) {
	a := map[string]*noderec.EngineRouting{"ollama": {Draining: true}}
	b := map[string]*noderec.EngineRouting{"ollama": {Draining: false}}
	if sameRouting(a, b) {
		t.Fatal("different draining should not be equal")
	}
}
