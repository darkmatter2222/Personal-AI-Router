// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import "testing"

var mutatingOps = []LifecycleOp{
	OpInstall, OpUninstall, OpStart, OpStop, OpRestart, OpReconfigure,
	OpPullModel, OpLoadModel, OpUnloadModel, OpDeleteModel, OpSwitchModel, OpRestartAfterAction,
}

var readOnlyOps = []LifecycleOp{
	OpStatus, OpHealth, OpListModels, OpLoadedModels, OpRoutingMetadata,
}

func TestLifecycle_ExternalForbidsAllMutations(t *testing.T) {
	for _, op := range mutatingOps {
		if LifecycleExternal.Allows(op) {
			t.Errorf("external engine must forbid mutating op %q", op)
		}
		if !op.Mutating() || op.ReadOnly() {
			t.Errorf("op %q should be classified mutating", op)
		}
	}
}

func TestLifecycle_ExternalAllowsReadOnly(t *testing.T) {
	for _, op := range readOnlyOps {
		if !LifecycleExternal.Allows(op) {
			t.Errorf("external engine must permit read-only op %q", op)
		}
		if !op.ReadOnly() || op.Mutating() {
			t.Errorf("op %q should be classified read-only", op)
		}
	}
}

func TestLifecycle_ManagedAllowsEverything(t *testing.T) {
	all := append(append([]LifecycleOp{}, mutatingOps...), readOnlyOps...)
	for _, op := range all {
		if !LifecycleManaged.Allows(op) {
			t.Errorf("managed engine must permit op %q", op)
		}
	}
}

func TestLifecycle_EngineRoutingAllows(t *testing.T) {
	ext := EngineRouting{Lifecycle: LifecycleExternal}
	managed := EngineRouting{} // zero mode = managed
	if ext.Allows(OpStart) {
		t.Fatal("external EngineRouting must forbid start")
	}
	if !ext.Allows(OpStatus) {
		t.Fatal("external EngineRouting must allow status")
	}
	if !managed.Allows(OpStart) {
		t.Fatal("managed EngineRouting must allow start")
	}
}
