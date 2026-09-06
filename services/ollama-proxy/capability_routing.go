// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"nvpair-shared/noderec"
	"nvpair-shared/routing"
)

// ensurePool returns the persistent static-capacity pool for a node, creating
// it (from the node's declared static capacity) on first use. Pools persist
// for the proxy's lifetime so reservations accumulate across concurrent
// requests rather than being recreated per request.
func (p *Proxy) ensurePool(nodeID string, staticCapacity int) *routing.Pool {
	p.capacityMu.Lock()
	defer p.capacityMu.Unlock()
	if pool, ok := p.capacityPools[nodeID]; ok {
		return pool
	}
	pool := routing.NewPool(staticCapacity)
	p.capacityPools[nodeID] = pool
	return pool
}

// staticCapacity reads a node's declared static capacity, nil-safe.
func staticCapacity(r *noderec.EngineRouting) int {
	if r == nil {
		return 0
	}
	return r.StaticCapacity
}

// timeoutSpec extracts the declared per-endpoint timeout spec, nil-safe.
func timeoutSpec(r *noderec.EngineRouting) *noderec.EngineTimeouts {
	if r == nil {
		return nil
	}
	return r.Timeouts
}

// expandModelKeys adds normalized model keys (ollamaModelKey) to the served
// list so the routing layer's exact-match ServesModel can match both the raw
// name ("llama") and its canonical form ("llama:latest"). This preserves the
// legacy proxy's model-availability semantics without changing the shared
// routing package's exact-match contract.
func expandModelKeys(served []string) []string {
	if len(served) == 0 {
		return served
	}
	seen := make(map[string]bool, len(served))
	out := make([]string, 0, len(served)*2)
	for _, m := range served {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
		key := ollamaModelKey(m)
		if key != m && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
		// Also add the bare name (strip :tag) so a request for "llama"
		// matches a node that only advertises "llama:latest".
		bare := strings.SplitN(key, ":", 2)
		if len(bare) == 2 && bare[0] != "" && !seen[bare[0]] {
			seen[bare[0]] = true
			out = append(out, bare[0])
		}
	}
	return out
}

// routeInference applies capability-aware deterministic selection to the
// candidate list for an inference request. It classifies the request, gates
// candidates by declared capabilities and context, selects the best eligible
// endpoint by declared priority, and reserves its static capacity. It returns
// the reordered candidate list (selected first), the diagnostic outcome, and
// the pool reserved by the selection (nil when nothing was eligible) so the
// caller can release it on every terminal path.
func (p *Proxy) routeInference(candidates []candidate, body []byte) ([]candidate, routing.Outcome, *routing.Pool) {
	req := routing.Classify(body)

	ranked := make([]routing.Candidate, 0, len(candidates))
	candByID := make(map[string]candidate, len(candidates))
	for _, c := range candidates {
		served := expandModelKeys(c.served)
		base := routing.RoutingToCandidate(c.routing, served)
		st, known := p.endpointStateLocked(c.id)

		// Merge the two lifecycle sources: the async-maintained health facet
		// (endpointState) and the manifest-declared admission flags
		// (EngineRouting.Enabled / .Draining). Absent routing metadata means
		// the endpoint is fully admitted (enabled, not draining, healthy).
		if known {
			base.Healthy = st.Healthy
			base.Enabled = base.Enabled && st.Enabled
			base.Draining = base.Draining || st.Draining
		} else {
			base.Healthy = true
		}

		base.ID = c.id
		base.Capacity = p.ensurePool(c.id, staticCapacity(c.routing))
		ranked = append(ranked, base)
		candByID[c.id] = c
	}

	out := routing.Select(ranked, req)

	var reservedPool *routing.Pool
	// Reorder so the selected candidate is tried first; when nothing is
	// eligible, keep the original order so the existing failover still runs.
	reordered := make([]candidate, 0, len(candidates))
	if out.SelectedID != "" {
		if sel, ok := candByID[out.SelectedID]; ok {
			reordered = append(reordered, sel)
			reservedPool = p.ensurePool(sel.id, staticCapacity(sel.routing))
		}
	}
	for _, c := range candidates {
		if c.id != out.SelectedID {
			reordered = append(reordered, c)
		}
	}
	if out.SelectedID == "" {
		reordered = candidates
	}
	slog.Debug("capability routing decision",
		"selected", out.SelectedID,
		"reasons", out.Reasons)
	return reordered, out, reservedPool
}

// endpointStateLocked returns a node's endpoint state and whether it was
// explicitly tracked.
func (p *Proxy) endpointStateLocked(id string) (endpointState, bool) {
	p.endpointMu.RLock()
	defer p.endpointMu.RUnlock()
	st, ok := p.endpointState[id]
	return st, ok
}

// setEndpointState records a node's async-maintained lifecycle/health facet.
func (p *Proxy) setEndpointState(id string, st endpointState) {
	p.endpointMu.Lock()
	p.endpointState[id] = st
	p.endpointMu.Unlock()
}

// rewriteModelAlias rewrites the request body's model field to the endpoint's
// physical model name when the requested model matches one of the endpoint's
// declared logical aliases. If the model is not an alias or the physical name,
// the body is returned unchanged. This is how one stable client-facing logical
// name maps to different physical model IDs on different runtimes.
func rewriteModelAlias(body []byte, routing *noderec.EngineRouting, requestedModel string) []byte {
	if routing == nil || routing.ModelRef == nil || requestedModel == "" {
		return body
	}
	physical := routing.ModelRef.PhysicalName
	isAlias := requestedModel == physical
	for _, a := range routing.ModelRef.Aliases {
		if requestedModel == a {
			isAlias = true
			break
		}
	}
	if !isAlias {
		return body
	}
	var payload map[string]json.RawMessage
	if len(body) == 0 {
		return body
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	payload["model"] = mustMarshal(physical)
	out, _ := json.Marshal(payload)
	return out
}

// mustMarshal is a small helper that marshals a value, ignoring errors on the
// hot path (a string always marshals cleanly).
func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// authHeadersForCand returns the locally-resolved auth headers for the engine
// this proxy fronts, or nil when none are configured. The values are env-expanded
// at load time and never leave the node or appear in logs.
func (p *Proxy) authHeadersForCand(c candidate) map[string]string {
	p.authMu.Lock()
	defer p.authMu.Unlock()
	return p.authHeaders[workloadEngine]
}

// applyAuthHeaders adds the endpoint's declared local auth headers (e.g.
// Authorization) to the outgoing request. The header values are resolved
// locally (env-expanded) and never leave the node.
func applyAuthHeaders(req *http.Request, headers map[string]string) {
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
}
