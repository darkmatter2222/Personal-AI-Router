// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"nvpair-shared/routing"
)

// regWith builds a registry holding the given manifests, keyed by engine name.
func regWith(ms ...*Manifest) *Registry {
	engines := make(map[string]*Manifest, len(ms))
	for _, m := range ms {
		engines[m.Engine] = m
	}
	return &Registry{engines: engines}
}

// TestExternalLifecycleGuard_RefusesMutationsThroughProductionEntryPoints proves
// the external-lifecycle guard is enforced by the actual Executor entry points
// (Start/Stop/Restart/Install/Uninstall/SetPort), not merely by the helper.
// The guard short-circuits before any process/state work, so a minimal Executor
// (only its registry set) is enough to exercise the refusal path faithfully.
func TestExternalLifecycleGuard_RefusesMutationsThroughProductionEntryPoints(t *testing.T) {
	ext := &Manifest{Engine: "ext-runtime", Routing: &routing.EngineRouting{Lifecycle: routing.LifecycleExternal}}
	managed := &Manifest{Engine: "managed-runtime"} // no routing block -> managed
	e := &Executor{reg: regWith(ext, managed)}
	ctx := context.Background()

	// The production lifecycle entry points must refuse for an external engine.
	if err := e.Start(ctx, "ext-runtime"); !errors.Is(err, ErrExternalLifecycle) {
		t.Errorf("Start(external) = %v, want ErrExternalLifecycle", err)
	}
	if err := e.Stop("ext-runtime"); !errors.Is(err, ErrExternalLifecycle) {
		t.Errorf("Stop(external) = %v, want ErrExternalLifecycle", err)
	}
	if err := e.Restart(ctx, "ext-runtime"); !errors.Is(err, ErrExternalLifecycle) {
		t.Errorf("Restart(external) = %v, want ErrExternalLifecycle", err)
	}
	if err := e.Install(ctx, "ext-runtime"); !errors.Is(err, ErrExternalLifecycle) {
		t.Errorf("Install(external) = %v, want ErrExternalLifecycle", err)
	}
	if err := e.Uninstall(ctx, "ext-runtime"); !errors.Is(err, ErrExternalLifecycle) {
		t.Errorf("Uninstall(external) = %v, want ErrExternalLifecycle", err)
	}
	if _, err := e.SetPort(ctx, "ext-runtime", 1234); !errors.Is(err, ErrExternalLifecycle) {
		t.Errorf("SetPort(external) = %v, want ErrExternalLifecycle", err)
	}
}

// TestExternalLifecycleGuard_HelperMatrix exercises the guard predicate directly
// across the full op matrix (managed allows all; external allows only read-only).
func TestExternalLifecycleGuard_HelperMatrix(t *testing.T) {
	ext := &Manifest{Engine: "ext", Routing: &routing.EngineRouting{Lifecycle: routing.LifecycleExternal}}
	managed := &Manifest{Engine: "managed"}
	e := &Executor{reg: regWith(ext, managed)}

	mutations := []routing.LifecycleOp{
		routing.OpInstall, routing.OpUninstall, routing.OpStart, routing.OpStop,
		routing.OpRestart, routing.OpReconfigure, routing.OpPullModel, routing.OpLoadModel,
		routing.OpUnloadModel, routing.OpDeleteModel, routing.OpSwitchModel, routing.OpRestartAfterAction,
	}
	readonly := []routing.LifecycleOp{
		routing.OpStatus, routing.OpHealth, routing.OpListModels, routing.OpLoadedModels, routing.OpRoutingMetadata,
	}
	for _, op := range mutations {
		if err := e.guardOp("ext", op); !errors.Is(err, ErrExternalLifecycle) {
			t.Errorf("external mutation %q allowed (err=%v)", op, err)
		}
		if err := e.guardOp("managed", op); err != nil {
			t.Errorf("managed mutation %q refused (err=%v)", op, err)
		}
	}
	for _, op := range readonly {
		if err := e.guardOp("ext", op); err != nil {
			t.Errorf("external read-only %q refused (err=%v)", op, err)
		}
	}

	if !e.externalEngine("ext") {
		t.Error("ext must be external")
	}
	if e.externalEngine("managed") {
		t.Error("managed must not be external")
	}
	if e.externalEngine("unknown-engine") {
		t.Error("an unknown engine must default to managed, not external")
	}
}

// TestExternalEngine_ActionGuardBlocksMutationBeforeExecution is the critical
// Phase-7 test: a dangerous mutating action on an external engine is refused by
// the production Action entry point BEFORE any command could run. The guard
// resolves from the registry alone, so a minimal Executor exercises the real
// refusal path; the fake command (which would fail loudly if ever executed) is
// never reached.
func TestExternalEngine_ActionGuardBlocksMutationBeforeExecution(t *testing.T) {
	ext := &Manifest{
		Engine:   "ext-runtime",
		Routing:  &routing.EngineRouting{Lifecycle: routing.LifecycleExternal},
		Actions: map[string]Action{
			// A mutating action with a command that must NEVER run.
			"pull_model":  {Cmd: []string{"THIS-COMMAND-MUST-NEVER-RUN", "--yes"}},
			"delete_model": {RemovePath: &ActionRemovePath{Path: "{model}", Root: "{models_dir}"}},
			"run_model":   {HTTP: &ActionHTTP{Method: "POST", Path: "/generate"}},
			// A read-only action IS permitted past the guard.
			"list_models": {HTTP: &ActionHTTP{Method: "GET", Path: "/v1/models"}, ReadOnly: true},
		},
	}
	e := &Executor{reg: regWith(ext)}
	ctx := context.Background()
	for _, a := range []string{"pull_model", "delete_model", "run_model"} {
		if _, err := e.Action(ctx, "ext-runtime", a, nil); !errors.Is(err, ErrExternalLifecycle) {
			t.Fatalf("mutating action %q on external engine must be refused, got %v", a, err)
		}
	}
	// The guard decision itself: read-only allowed, mutating/unknown refused.
	if !e.actionAllowedForExternal("ext-runtime", "list_models") {
		t.Fatal("read-only action must be allowed on an external engine")
	}
	if e.actionAllowedForExternal("ext-runtime", "pull_model") {
		t.Fatal("mutating action must be refused on an external engine")
	}
	if e.actionAllowedForExternal("ext-runtime", "no_such_action") {
		t.Fatal("unknown action must be refused on an external engine")
	}
	// A managed engine allows any action past this guard (subject to its manifest).
	managed := &Manifest{Engine: "managed", Actions: map[string]Action{"pull_model": {Cmd: []string{"x"}}}}
	em := &Executor{reg: regWith(managed)}
	if !em.actionAllowedForExternal("managed", "pull_model") {
		t.Fatal("managed engine must allow its actions past the external guard")
	}
}

// TestResolveActionTimeout proves the declarative action-timeout contract
// (Phase 5): explicit per-action timeout_ms wins, then the engine's routing
// ActionMS, then the global default.
func TestResolveActionTimeout(t *testing.T) {
	e := &Executor{actionTimeout: 30 * time.Minute}

	if got := e.resolveActionTimeout(&engineState{manifest: &Manifest{}}, Action{TimeoutMS: 5000}); got != 5*time.Second {
		t.Fatalf("per-action timeout_ms = %v, want 5s", got)
	}
	routed := &engineState{manifest: &Manifest{Routing: &routing.EngineRouting{Timeouts: routing.Timeouts{ActionMS: 60000}}}}
	if got := e.resolveActionTimeout(routed, Action{}); got != 60*time.Second {
		t.Fatalf("routing ActionMS = %v, want 60s", got)
	}
	if got := e.resolveActionTimeout(&engineState{manifest: &Manifest{}}, Action{}); got != 30*time.Minute {
		t.Fatalf("default action timeout = %v, want 30m", got)
	}
	// Per-action wins over routing.
	both := &engineState{manifest: &Manifest{Routing: &routing.EngineRouting{Timeouts: routing.Timeouts{ActionMS: 60000}}}}
	if got := e.resolveActionTimeout(both, Action{TimeoutMS: 1000}); got != time.Second {
		t.Fatalf("per-action must win over routing, got %v", got)
	}
}
