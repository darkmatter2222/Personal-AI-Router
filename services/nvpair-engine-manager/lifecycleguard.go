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
// existing behaviour for the built-in engines.
func (e *Executor) engineLifecycle(engine string) routing.LifecycleMode {
	if mf, ok := e.reg.Get(engine); ok && mf.Routing != nil {
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
