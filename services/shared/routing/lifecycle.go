// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

// LifecycleOp names a lifecycle operation an engine manager can perform on an
// engine. It is the vocabulary the external-lifecycle guard reasons about.
type LifecycleOp string

const (
	// Mutating operations — forbidden on an external (adopt-only) engine.
	OpInstall            LifecycleOp = "install"
	OpUninstall          LifecycleOp = "uninstall"
	OpStart              LifecycleOp = "start"
	OpStop               LifecycleOp = "stop"
	OpRestart            LifecycleOp = "restart"
	OpReconfigure        LifecycleOp = "reconfigure" // change command line / port / args
	OpPullModel          LifecycleOp = "pull"
	OpLoadModel          LifecycleOp = "load"
	OpUnloadModel        LifecycleOp = "unload"
	OpDeleteModel        LifecycleOp = "delete"
	OpSwitchModel        LifecycleOp = "switch-model"
	OpRestartAfterAction LifecycleOp = "restart-after-action"

	// Read-only operations — always permitted, including on an external engine.
	OpStatus          LifecycleOp = "status"
	OpHealth          LifecycleOp = "health"
	OpListModels      LifecycleOp = "list-models"
	OpLoadedModels    LifecycleOp = "loaded-models"
	OpRoutingMetadata LifecycleOp = "routing-metadata"
)

// ReadOnly reports whether the operation only observes the engine and never
// mutates its process, files, configuration or loaded models.
func (op LifecycleOp) ReadOnly() bool {
	switch op {
	case OpStatus, OpHealth, OpListModels, OpLoadedModels, OpRoutingMetadata:
		return true
	default:
		return false
	}
}

// Mutating reports the inverse of ReadOnly.
func (op LifecycleOp) Mutating() bool { return !op.ReadOnly() }

// Allows reports whether an engine with this lifecycle mode permits op.
//
//   - A managed engine (the default/zero mode) permits every operation. Whether
//     a specific managed engine can actually perform an operation is a separate
//     question answered by that engine's manifest capabilities; this guard does
//     not grant capabilities, it only withholds them from external engines.
//   - An external (adopt-only) engine permits ONLY read-only operations. Every
//     mutating operation — install, uninstall, start, stop, restart,
//     reconfigure, pull, load, unload, delete, switch-model and
//     restart-after-action — is refused, because PAIR observes and routes to an
//     external backend but never owns its lifecycle.
func (m LifecycleMode) Allows(op LifecycleOp) bool {
	if !m.External() {
		return true
	}
	return op.ReadOnly()
}

// Allows reports whether the engine's declared lifecycle permits op. It is the
// convenience the engine manager calls at each mutation entry point:
//
//	if !manifestRouting.Allows(routing.OpStart) { return ErrExternalLifecycle }
func (e EngineRouting) Allows(op LifecycleOp) bool {
	return e.Lifecycle.Allows(op)
}
