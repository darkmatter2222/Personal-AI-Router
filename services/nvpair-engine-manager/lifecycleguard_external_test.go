// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"testing"

	"nvpair-shared/routing"
)

// externalRuntimeManifest declares the external runtime mode but NO routing
// block, so it proves the guard derives adopt-only status from the runtime mode
// alone (the omitted-routing bypass is closed).
func externalRuntimeManifest(engine string, actions map[string]Action) *Manifest {
	return &Manifest{
		Engine:    engine,
		Platforms: map[string]Platform{"linux/amd64": {Runtime: Runtime{Mode: "external", Port: 8000}}},
		Actions:   actions,
	}
}

func TestRuntimeExternal_IsAdoptOnlyWithoutRoutingBlock(t *testing.T) {
	ext := externalRuntimeManifest("ext-rt", nil)
	e := &Executor{reg: regWith(ext)}

	if got := e.engineLifecycle("ext-rt"); got != routing.LifecycleExternal {
		t.Fatalf("runtime.mode=external must be adopt-only even without a routing block; got %q", got)
	}
	if !e.externalEngine("ext-rt") {
		t.Fatal("externalEngine must report true for a runtime-external engine")
	}

	// Production lifecycle mutations must refuse before any process work.
	ctx := context.Background()
	if err := e.Start(ctx, "ext-rt"); !errors.Is(err, ErrExternalLifecycle) {
		t.Errorf("Start(runtime-external) = %v, want ErrExternalLifecycle", err)
	}
	if err := e.Stop("ext-rt"); !errors.Is(err, ErrExternalLifecycle) {
		t.Errorf("Stop(runtime-external) = %v, want ErrExternalLifecycle", err)
	}
}

func TestRuntimeExternal_ActionGuardAllowsOnlyReadOnly(t *testing.T) {
	// regWith stores the manifest without validating it, so this exercises the
	// runtime guard directly (defense in depth) across a read-only and a mutating
	// action on the same adopt-only engine.
	ext := externalRuntimeManifest("ext-rt", map[string]Action{
		"list":   {HTTP: &ActionHTTP{Method: "GET", Path: "/v1/models"}, ReadOnly: true},
		"reload": {HTTP: &ActionHTTP{Method: "POST", Path: "/reload"}, ReadOnly: false},
	})
	e := &Executor{reg: regWith(ext)}

	if !e.actionAllowedForExternal("ext-rt", "list") {
		t.Error("a read-only action must be allowed on an adopt-only engine")
	}
	if e.actionAllowedForExternal("ext-rt", "reload") {
		t.Error("a non-read-only action must be refused on an adopt-only engine")
	}
	if e.actionAllowedForExternal("ext-rt", "unknown") {
		t.Error("an unknown action must be refused on an adopt-only engine")
	}
}
