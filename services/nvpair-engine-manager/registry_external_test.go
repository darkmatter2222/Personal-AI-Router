// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

// These tests exercise pure manifest validation (no process, no network, no
// engine). They cover the external/adopt runtime mode (Phase 9) and the action
// contradiction rules (Phase 10/33).

func extManifest(engine string, p Platform, actions map[string]Action) *Manifest {
	return &Manifest{
		Engine:          engine,
		DisplayName:     "Test",
		ManifestVersion: 1,
		Platforms:       map[string]Platform{"linux/amd64": p},
		Actions:         actions,
	}
}

func externalRuntime() Runtime {
	return Runtime{Mode: "external", Port: 8000, Health: &Probe{HTTP: "http://127.0.0.1:8000/health"}}
}

func mustReject(t *testing.T, m *Manifest, wantSubstr string) {
	t.Helper()
	err := m.Validate()
	if err == nil {
		t.Fatalf("expected validation error containing %q, got nil", wantSubstr)
	}
	if wantSubstr != "" && !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("error %q does not contain %q", err.Error(), wantSubstr)
	}
}

func mustAccept(t *testing.T, m *Manifest) {
	t.Helper()
	if err := m.Validate(); err != nil {
		t.Fatalf("expected valid manifest, got error: %v", err)
	}
}

func TestExternalRuntime_PortAndHealthIsValidWithoutBin(t *testing.T) {
	// "I already have vLLM on port 8000; adopt it": no bin, no start, just a port
	// and a health probe.
	m := extManifest("engine-adopt-test", Platform{Runtime: externalRuntime()}, nil)
	mustAccept(t, m)
}

func TestExternalRuntime_PortOnlyIsValid(t *testing.T) {
	m := extManifest("engine-adopt-test", Platform{Runtime: Runtime{Mode: "external", Port: 8000}}, nil)
	mustAccept(t, m)
}

func TestExternalRuntime_PortRequired(t *testing.T) {
	m := extManifest("engine-adopt-test", Platform{Runtime: Runtime{Mode: "external"}}, nil)
	mustReject(t, m, "runtime.port is required")
}

func TestManagedProcess_RequiresBin(t *testing.T) {
	// A managed process engine still requires a bin — external mode does not
	// loosen the managed contract.
	m := extManifest("engine-managed-test", Platform{Runtime: Runtime{Mode: "process"}}, nil)
	mustReject(t, m, "runtime.bin is required")
}

func TestExternalRuntime_StartRejected(t *testing.T) {
	r := externalRuntime()
	r.Start = [][]string{{"serve"}}
	m := extManifest("engine-adopt-test", Platform{Runtime: r}, nil)
	mustReject(t, m, "runtime.start is not allowed in external mode")
}

func TestExternalRuntime_StopRejected(t *testing.T) {
	r := externalRuntime()
	r.Stop = &StopSpec{Signal: "term"}
	m := extManifest("engine-adopt-test", Platform{Runtime: r}, nil)
	mustReject(t, m, "runtime.stop is not allowed in external mode")
}

func TestExternalRuntime_BinRejected(t *testing.T) {
	r := externalRuntime()
	r.Bin = "/usr/bin/vllm"
	m := extManifest("engine-adopt-test", Platform{Runtime: r}, nil)
	mustReject(t, m, "runtime.bin must be empty in external mode")
}

func TestExternalRuntime_InstallRejected(t *testing.T) {
	m := extManifest("engine-adopt-test", Platform{Install: &Install{}, Runtime: externalRuntime()}, nil)
	mustReject(t, m, "install is not allowed in external mode")
}

func TestExternalRuntime_UninstallRejected(t *testing.T) {
	m := extManifest("engine-adopt-test", Platform{Uninstall: &Uninstall{Run: []string{"x"}}, Runtime: externalRuntime()}, nil)
	mustReject(t, m, "uninstall is not allowed in external mode")
}

// --- action contradiction rules (Phase 10/33) --------------------------------

func processPlatform() Platform {
	return Platform{Runtime: Runtime{Bin: "/usr/bin/engine"}}
}

func TestAction_NegativeTimeoutRejected(t *testing.T) {
	m := extManifest("engine-managed-test", processPlatform(), map[string]Action{
		"pull": {HTTP: &ActionHTTP{Method: "POST", Path: "/pull"}, TimeoutMS: -1},
	})
	mustReject(t, m, "timeout_ms must not be negative")
}

func TestAction_ReadOnlyRestartAfterRejected(t *testing.T) {
	m := extManifest("engine-managed-test", processPlatform(), map[string]Action{
		"obs": {HTTP: &ActionHTTP{Method: "GET", Path: "/models"}, ReadOnly: true, RestartAfter: true},
	})
	mustReject(t, m, "read_only is incompatible with restart_after")
}

func TestAction_ReadOnlyRemovePathRejected(t *testing.T) {
	m := extManifest("engine-managed-test", processPlatform(), map[string]Action{
		"del": {RemovePath: &ActionRemovePath{Path: "{models_dir}/x", Root: "{models_dir}"}, ReadOnly: true},
	})
	mustReject(t, m, "read_only is incompatible with remove_path")
}

func TestAction_SlowLoadNonHTTPRejected(t *testing.T) {
	m := extManifest("engine-managed-test", processPlatform(), map[string]Action{
		"list": {Cmd: []string{"engine", "ls"}, SlowLoad: true},
	})
	mustReject(t, m, "slow_load applies only to an http action")
}

func TestExternalEngine_NonReadOnlyActionRejected(t *testing.T) {
	// An external engine that declares a mutating (non-read-only) action can never
	// run it past the guard, so it is rejected at load.
	m := extManifest("engine-adopt-test", Platform{Runtime: externalRuntime()}, map[string]Action{
		"restart": {HTTP: &ActionHTTP{Method: "POST", Path: "/restart"}, ReadOnly: false},
	})
	mustReject(t, m, "external (adopt-only) engine may declare only read_only actions")
}

func TestExternalEngine_ReadOnlyActionsValid(t *testing.T) {
	m := extManifest("engine-adopt-test", Platform{Runtime: externalRuntime()}, map[string]Action{
		"list":   {HTTP: &ActionHTTP{Method: "GET", Path: "/v1/models"}, ReadOnly: true},
		"loaded": {HTTP: &ActionHTTP{Method: "GET", Path: "/v1/models"}, ReadOnly: true, Result: &ActionResult{Array: "data", Field: "id"}},
	})
	mustAccept(t, m)
}
