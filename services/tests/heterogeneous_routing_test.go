// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Cross-process tests proving the scheduler's engine set is open: a
// heterogeneous fleet can schedule workloads for any engine name (vLLM, torch,
// a custom runtime) without a code change to the scheduler. The control plane
// (discovery/nodes-changed) and the data plane (workload upserts) are driven
// over real JSON-RPC to the scheduler binary.
package tests

import (
	"encoding/json"
	"testing"
	"time"

	"nvpair-shared/jsonrpc"
	"nvpair-shared/schedulerwire"
)

// waitForEnginePriority waits for the scheduler's schedule:priority emission
// whose engine matches targetEngine, and returns the parsed EnginePriority.
func waitForEnginePriority(t *testing.T, ch <-chan jsonrpc.Message, targetEngine string, timeout time.Duration) schedulerwire.EnginePriority {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed before %q priority emission", targetEngine)
			}
			if msg.Method != "schedule:priority" {
				continue
			}
			var p schedulerwire.EnginePriority
			if json.Unmarshal(msg.Params, &p) != nil {
				continue
			}
			if p.Engine == targetEngine {
				return p
			}
		case <-timer.C:
			t.Fatalf("timed out (%s) waiting for %q schedule:priority", timeout, targetEngine)
		}
	}
}

// TestHeterogeneousSchedulerOpenEngineSet drives a three-node cluster and an
// engine the scheduler has never seen ("vllm"). The scheduler must rank the
// nodes node-wide and emit a schedule:priority for the unknown engine, proving
// the engine set is open: no change to schedule.go is required to add vLLM.
func TestHeterogeneousSchedulerOpenEngineSet(t *testing.T) {
	stdin, msgs, cleanup := startSchedulerProc(t, "--interval", "1h")
	t.Cleanup(cleanup)

	waitForMethod(t, msgs, "ready", 10*time.Second)

	// Multi-node cluster with three heterogeneous nodes.
	writeRawFrame(t, stdin, `{"jsonrpc":"2.0","method":"discovery:nodes-changed","params":[{"hostUuid":"node-a"},{"hostUuid":"node-b"},{"hostUuid":"node-c"}]}`)

	// The two built-in engine outputs are emitted first.
	waitForPriorityPair(t, msgs, 5*time.Second)

	// A workload for an engine the scheduler has never seen: "vllm".
	writeRawFrame(t, stdin, `{"jsonrpc":"2.0","method":"workloads:upsert","params":{"workloadInfo":{"id":"wl-vllm","engine":"vllm","runId":"r","state":"running","originatedFrom":"x","scheduledOn":"node-a"}}}`)

	// The scheduler must emit a priority list for the unknown engine, ranking
	// the three nodes by load. node-a carries the single vLLM workload, so it
	// ranks last.
	p := waitForEnginePriority(t, msgs, "vllm", 5*time.Second)
	if p.Engine != "vllm" {
		t.Fatalf("emitted engine = %q, want vllm", p.Engine)
	}
	wantOrder := []string{"node-b", "node-c", "node-a"}
	if len(p.Nodes) != len(wantOrder) {
		t.Fatalf("vllm nodes = %v, want %v", p.Nodes, wantOrder)
	}
	for i := range wantOrder {
		if p.Nodes[i] != wantOrder[i] {
			t.Fatalf("vllm nodes = %v, want %v", p.Nodes, wantOrder)
		}
	}
}
