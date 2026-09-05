// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"nvpair-shared/noderec"
)

// RoutingToEndpoint maps the declarative routing metadata (noderec.EngineRouting)
// onto the routing package's endpoint snapshot: capability flags, static
// capacity pool, deterministic priority, and max context. A nil EngineRouting
// yields a zero-value endpoint (no declared capabilities, unbounded pool).
func RoutingToEndpoint(r *noderec.EngineRouting) (caps EndpointCaps, pool *Pool, priority int) {
	if r == nil {
		return EndpointCaps{}, NewPool(0), 0
	}
	caps.MaxContext = r.ContextMaxTokens
	priority = r.Priority
	pool = NewPool(r.StaticCapacity)
	if c := r.Capabilities; c != nil {
		caps.Text = c.Text
		caps.Vision = c.Vision
		caps.Tools = c.Tools
		caps.Streaming = c.Streaming
		caps.Reasoning = c.Reasoning
	}
	return caps, pool, priority
}
