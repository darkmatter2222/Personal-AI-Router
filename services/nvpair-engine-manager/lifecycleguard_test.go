// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"testing"

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
