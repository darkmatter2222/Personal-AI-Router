// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"testing"
	"time"
)

// TestOpenEngineSet_DedupsAcrossModelAndRoutingKeys: an engine id advertised in
// BOTH modelsByEngine and routingByEngine on the same node appears exactly once
// in the emitted engine set (not double-counted).
func TestOpenEngineSet_DedupsAcrossModelAndRoutingKeys(t *testing.T) {
	rec := &capRW{}
	m := NewManager(NewCodec(rec), 24*time.Hour)
	m.handleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "discovery:nodes-changed",
		Params:  json.RawMessage(`[{"hostUuid":"a","modelsByEngine":{"dup-engine":["m"]},"routingByEngine":{"dup-engine":{}}}]`),
	})
	if got := rec.orders("dup-engine"); len(got) == 0 {
		t.Fatal("engine present in both maps should receive priority")
	}
	count := 0
	for _, e := range m.engineList() {
		if e == "dup-engine" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("dup-engine appears %d times in engineList, want exactly 1", count)
	}
}

// TestOpenEngineSet_EngineOnOneNodeStillEmits: an engine advertised by only one
// of several nodes is still in the emitted set.
func TestOpenEngineSet_EngineOnOneNodeStillEmits(t *testing.T) {
	rec := &capRW{}
	m := NewManager(NewCodec(rec), 24*time.Hour)
	m.handleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "discovery:nodes-changed",
		Params: json.RawMessage(`[
			{"hostUuid":"a","modelsByEngine":{"only-here":["m"]}},
			{"hostUuid":"b","modelsByEngine":{"ollama":["x"]}}
		]`),
	})
	if got := rec.orders("only-here"); len(got) == 0 {
		t.Fatal("an engine advertised by a single node must still receive priority")
	}
}
