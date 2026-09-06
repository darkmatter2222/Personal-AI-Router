// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"slices"
	"testing"
	"time"
)

// Open-engine-set tests: the scheduler's activeEngines() must include the
// built-in defaults (ollama, lmstudio) plus any engine name that appears in
// the workload catalog, proving a heterogeneous fleet is scheduled without
// hard-coding each new engine.

func TestActiveEngines_DefaultsOnly(t *testing.T) {
	m := NewManager(NewCodec(nopRW{}), time.Second)
	got := m.activeEngines()
	want := []string{"lmstudio", "ollama"}
	assertStrsSorted(t, got, want)
}

func TestActiveEngines_CatalogDerivedEngine(t *testing.T) {
	m := NewManager(NewCodec(nopRW{}), time.Second)
	// A workload for an unknown engine "vllm" appears in the catalog.
	m.catalog[wlKey{origin: "host-a", id: "w1"}] = wl("w1", "vllm", "running", "host-a", "host-a")
	got := m.activeEngines()
	want := []string{"lmstudio", "ollama", "vllm"}
	assertStrsSorted(t, got, want)
}

func TestActiveEngines_MultipleCustomEngines(t *testing.T) {
	m := NewManager(NewCodec(nopRW{}), time.Second)
	m.catalog[wlKey{origin: "host-a", id: "w1"}] = wl("w1", "vllm", "running", "host-a", "host-a")
	m.catalog[wlKey{origin: "host-b", id: "w2"}] = wl("w2", "torch-runtime", "running", "host-b", "host-b")
	m.catalog[wlKey{origin: "host-c", id: "w3"}] = wl("w3", "my-custom-inference", "queued", "host-c", "host-c")
	got := m.activeEngines()
	want := []string{"lmstudio", "my-custom-inference", "ollama", "torch-runtime", "vllm"}
	assertStrsSorted(t, got, want)
}

func TestActiveEngines_Deduplication(t *testing.T) {
	m := NewManager(NewCodec(nopRW{}), time.Second)
	// Multiple workloads for the same custom engine.
	m.catalog[wlKey{origin: "host-a", id: "w1"}] = wl("w1", "vllm", "running", "host-a", "host-a")
	m.catalog[wlKey{origin: "host-b", id: "w2"}] = wl("w2", "vllm", "queued", "host-b", "host-b")
	got := m.activeEngines()
	want := []string{"lmstudio", "ollama", "vllm"}
	assertStrsSorted(t, got, want)
}

func TestActiveEngines_EmptyEngineNameIgnored(t *testing.T) {
	m := NewManager(NewCodec(nopRW{}), time.Second)
	// A workload with an empty engine name should not add a blank entry.
	m.catalog[wlKey{origin: "host-a", id: "w1"}] = wl("w1", "", "running", "host-a", "host-a")
	got := m.activeEngines()
	want := []string{"lmstudio", "ollama"}
	assertStrsSorted(t, got, want)
}

func TestActiveEngines_DeterministicOrder(t *testing.T) {
	m := NewManager(NewCodec(nopRW{}), time.Second)
	m.catalog[wlKey{origin: "host-a", id: "w1"}] = wl("w1", "zeta-engine", "running", "host-a", "host-a")
	m.catalog[wlKey{origin: "host-b", id: "w2"}] = wl("w2", "alpha-engine", "running", "host-b", "host-b")
	got1 := m.activeEngines()
	got2 := m.activeEngines()
	if len(got1) != len(got2) {
		t.Fatalf("inconsistent length: %d vs %d", len(got1), len(got2))
	}
	for i := range got1 {
		if got1[i] != got2[i] {
			t.Fatalf("non-deterministic order: %v vs %v", got1, got2)
		}
	}
	// Must be sorted.
	if !slices.IsSorted(got1) {
		t.Fatalf("not sorted: %v", got1)
	}
}

func TestActiveEngines_RandomEngineNames(t *testing.T) {
	names := []string{
		"engine-test-1",
		"custom-openai-runtime",
		"my-vlm",
		"future-runtime-999",
		"a",
		"z",
		"abc-def-ghi",
	}
	m := NewManager(NewCodec(nopRW{}), time.Second)
	for i, name := range names {
		m.catalog[wlKey{origin: "host", id: string(rune('w' + i))}] = wl(string(rune('w'+i)), name, "running", "host", "host")
	}
	got := m.activeEngines()
	// Must contain all custom names plus the defaults.
	gotSet := make(map[string]bool)
	for _, e := range got {
		gotSet[e] = true
	}
	if !gotSet["ollama"] || !gotSet["lmstudio"] {
		t.Fatalf("defaults missing from %v", got)
	}
	for _, name := range names {
		if !gotSet[name] {
			t.Fatalf("custom engine %q missing from %v", name, got)
		}
	}
}

func assertStrsSorted(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
