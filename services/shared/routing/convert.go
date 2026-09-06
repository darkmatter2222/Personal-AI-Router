// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"nvpair-shared/noderec"
)

func truePtr() *bool { b := true; return &b }

// RoutingToCandidate maps declarative routing metadata (noderec.EngineRouting)
// plus the node's model inventory onto a routing Candidate snapshot:
// capability flags, static capacity pool, deterministic priority, per-model
// declarations, and the served/alias model set.
//
// Legacy semantics: a nil EngineRouting yields a fully-admitted,
// capability-undeclared candidate (enabled, unbounded capacity, priority 0,
// no model restrictions). A node that advertises a model inventory but no
// routing metadata for the engine is therefore routed exactly as before
// (backward compatibility): the model-availability gate sees the served list,
// and the capability gate sees no declarations, which a request that declares
// no requirements also passes.
func RoutingToCandidate(r *noderec.EngineRouting, servedModels []string) Candidate {
	c := Candidate{
		Enabled: true, // omitted Enabled defaults to enabled (legacy nodes)
		Served:  append([]string(nil), servedModels...),
		Aliases: AliasesOf(r),
		Models:  ModelRefs(r),
	}
	if r == nil {
		// Legacy endpoint: no capability declarations, so it passes all
		// gates (backward compatibility). Set every cap to true so the
		// eligibility check does not reject it for "undeclared" features.
		c.Capacity = NewPool(0)
		c.Caps = EndpointCaps{
			Text:      truePtr(),
			Vision:    truePtr(),
			Tools:     truePtr(),
			Streaming: truePtr(),
		}
		return c
	}
	c.APIFamily = r.APIFamily
	c.Strategy = r.Strategy
	c.Priority = r.Priority
	c.Capacity = NewPool(r.StaticCapacity)
	c.MaxContext = r.ContextMaxTokens
	if r.Enabled != nil {
		c.Enabled = *r.Enabled
	} else {
		// Omitted Enabled defaults to enabled: the wire zero value (false)
		// would otherwise invert the documented default.
		c.Enabled = true
	}
	c.Draining = r.Draining
	if r.Capabilities != nil {
		c.Caps.Text = r.Capabilities.Text
		c.Caps.Vision = r.Capabilities.Vision
		c.Caps.Tools = r.Capabilities.Tools
		c.Caps.Streaming = r.Capabilities.Streaming
		c.Caps.Reasoning = r.Capabilities.Reasoning
	}
	// When r != nil but r.Capabilities == nil, the endpoint declared routing
	// metadata (priority, family, auth) but no capability constraints. The
	// zero-value Caps (all nil) means "undeclared" = "unsupported" per the
	// eligibility gate: the endpoint opted into the capability system without
	// declaring what it supports, so it is gated out of capability-specific
	// requests. This is distinct from r == nil (legacy: no routing at all,
	// passes all gates via the truePtr defaults above).
	return c
}
