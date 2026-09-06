// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"

	"nvpair-shared/routing"
)

// ErrExternalLifecycle is returned when a lifecycle-mutating operation is
// attempted on an engine whose manifest declares an external (adopt-only)
// lifecycle. PAIR observes, health-checks and routes to such an engine but must
// never install, uninstall, start, stop, restart, reconfigure it, or otherwise
// own its process, files or loaded models. Read-only operations (status,
// health, model inventory, routing metadata) remain permitted.
var ErrExternalLifecycle = errors.New("engine is externally managed (adopt-only); lifecycle mutation is not permitted")

// engineLifecycle returns the declared lifecycle mode for an engine. An engine
// whose manifest declares no routing block is managed (the default), preserving
// existing behaviour for the built-in engines. An engine that declares the
// external runtime mode is adopt-only even without routing metadata, so the
// guard cannot be bypassed by omitting the routing block.
func (e *Executor) engineLifecycle(engine string) routing.LifecycleMode {
	mf, ok := e.reg.Get(engine)
	if !ok {
		return routing.LifecycleManaged
	}
	if mf.external() {
		return routing.LifecycleExternal
	}
	if mf.Routing != nil {
		return mf.Routing.Lifecycle
	}
	return routing.LifecycleManaged
}

// guardOp is the single external-lifecycle enforcement point every mutating
// entry point calls. It returns ErrExternalLifecycle when op is refused because
// the engine is external. The allow/deny decision itself is the pure, exhaustively
// unit-tested routing.LifecycleMode.Allows — this method only maps a refusal to
// an error.
func (e *Executor) guardOp(engine string, op routing.LifecycleOp) error {
	if e.engineLifecycle(engine).Allows(op) {
		return nil
	}
	return ErrExternalLifecycle
}

// externalEngine reports whether the engine is external/adopt-only, so bulk
// operations (shutdown's StopAll) can silently skip it rather than error.
func (e *Executor) externalEngine(engine string) bool {
	return e.engineLifecycle(engine).External()
}

// actionAllowedForExternal reports whether an action may run on the engine given
// its lifecycle. A managed engine permits every action (subject to its own
// manifest); an external/adopt-only engine permits ONLY actions explicitly
// declared read_only. It reads the registry alone (no engine state), so a
// mutating action is refused before any state access or execution. An external
// engine that is unknown, or an action that is unknown/unmarked, is refused.
func (e *Executor) actionAllowedForExternal(engine, action string) bool {
	if !e.externalEngine(engine) {
		return true
	}
	mf, ok := e.reg.Get(engine)
	if !ok {
		return false
	}
	a, ok := mf.Actions[action]
	return ok && a.ReadOnly
}
