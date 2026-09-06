// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package routeadapter wires the shared, engine-agnostic routing pipeline
// (routing.ClassifyRequest -> routing.DecideResolved -> routing.Pools ->
// routing.Forward) to a PAIR proxy's live control-plane state through small
// injected providers. Its purpose is to make a proxy's inference handler a thin
// call while keeping the whole decision-and-forward pipeline in one
// dependency-light place that is component-testable with fakes (a fake discovery
// snapshot, a fake scheduler order, a fake http.RoundTripper, an in-memory
// routing.Pools) and no socket, server, or engine.
//
// Cross-engine by construction. A Router is NOT scoped to a single engine id.
// One logical request (say model "local-coding") considers candidate endpoints
// across every engine a node declares — vllm on one node, llama.cpp on another,
// a custom OpenAI-compatible runtime on a third — because engine identity does
// not define the routing universe; the API family, the logical model/alias, the
// declared capabilities and the endpoint policy do. An optional EngineFilter
// narrows the engine set only where a surface genuinely needs it.
//
// Node vs endpoint identity. A node (one PAIR host, keyed by HostUUID) may host
// several engines. Each (node, engine) pair is a distinct endpoint with its own
// admission pool and target, but all endpoints on a node share the node's
// scheduler rank. The adapter builds the EndpointID with routing.EndpointKey so
// the pair cannot collide, sets NodeID to the host UUID, and resolves targets by
// EndpointID.
//
// Legacy seam. If NO candidate node advertises any routing metadata (for the
// filtered engine set), Route reports handled=false WITHOUT consuming the
// request body, so the proxy falls back to its existing behaviour. In a mixed
// cluster where at least one node is enhanced, legacy nodes do not vanish: the
// adapter synthesises a conservative candidate from a legacy node's advertised
// inventory (text-only, no invented vision/tools, unbounded, unknown context).
package routeadapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"nvpair-shared/noderec"
	"nvpair-shared/routing"
)

// DefaultMaxBody bounds the buffered request body when a Router does not set one.
// 16 MiB comfortably fits ordinary chat/completions traffic and a handful of
// base64 images; deployments that expect larger multimodal payloads should raise
// Router.MaxBody explicitly rather than relying on this default. The limit is
// enforced by explicit oversize detection (413), never by silent truncation.
const DefaultMaxBody = 16 << 20

// EngineFilter reports whether an engine id participates in routing for a given
// surface. A nil filter accepts every engine, which is the cross-engine default.
type EngineFilter func(engineID string) bool

func (f EngineFilter) accept(engine string) bool { return f == nil || f(engine) }

// EnhancedEndpoints materialises routing.Endpoints from the declared
// RoutingByEngine of every node, across ALL engines that pass the filter. It is
// the cross-engine candidate builder: a node advertising both "vllm" and
// "llamacpp" yields two endpoints with a shared NodeID (the host UUID) and
// distinct EndpointIDs (routing.EndpointKey(hostUUID, engine)), so their
// admission pools, targets and deterministic tie-breaks never collide while they
// share one scheduler rank.
//
// hasMetadata reports whether ANY node advertised routing metadata for a filtered
// engine; when false the caller should use its existing (legacy) routing rather
// than treat the whole cluster as ineligible.
//
// Phase 7 empty-Models contract: an engine that declares routing metadata but no
// explicit Models list falls back to its advertised inventory (ModelsByEngine),
// synthesising a conservative per-model contract (text-only, streaming, no
// invented vision/tools, unknown context) so the enhanced engine still routes
// its inventory models rather than serving nothing.
func EnhancedEndpoints(nodes []noderec.DirectoryNode, filter EngineFilter, healthy func(noderec.DirectoryNode) bool) (eps []routing.Endpoint, hasMetadata bool) {
	for _, n := range nodes {
		for engine, er := range n.RoutingByEngine {
			if !filter.accept(engine) {
				continue
			}
			hasMetadata = true
			ep := er.EndpointFor(n.HostUUID, routing.EndpointKey(n.HostUUID, engine), engine, nodeHealthy(n, healthy))
			if len(ep.Models) == 0 {
				ep.Models = permissiveModels(n.ModelsByEngine[engine])
			}
			eps = append(eps, ep)
		}
	}
	return eps, hasMetadata
}

// LegacyCandidates synthesises conservative endpoints for nodes that advertise
// the requested model in their inventory (ModelsByEngine) for an engine that has
// NO routing metadata. This is the mixed-cluster (rolling-upgrade) behaviour of
// Phase 6: a legacy peer does not disappear because another peer was upgraded.
//
// The synthesised contract preserves legacy semantics for ordinary text traffic
// (unbounded capacity, unknown/undeclared context, default strategy, physical ==
// inventory model, no model rewrite) but is deliberately conservative about
// capability: it advertises text and streaming only. It never INVENTS vision or
// tools support, so a capability-specific request is safely excluded from a
// legacy node rather than routed to it on a guess.
//
// The synthesised endpoint's API family is the surface family the caller serves.
// The caller decides which nodes/engines are candidates (via EngineFilter), so a
// legacy node reached through a given surface is assumed to speak that surface's
// family — the same assumption PAIR's engine-specific proxies already made.
func LegacyCandidates(nodes []noderec.DirectoryNode, filter EngineFilter, healthy func(noderec.DirectoryNode) bool, model string, family routing.APIFamily) []routing.Endpoint {
	if model == "" {
		return nil // nothing to match against; a modelless request routes nowhere
	}
	var eps []routing.Endpoint
	for _, n := range nodes {
		for engine, inv := range n.ModelsByEngine {
			if !filter.accept(engine) {
				continue
			}
			if _, enhanced := n.RoutingByEngine[engine]; enhanced {
				continue // this engine already contributed an enhanced endpoint
			}
			if !containsModel(inv, model) {
				continue
			}
			eps = append(eps, routing.Endpoint{
				ID:        routing.EndpointKey(n.HostUUID, engine),
				NodeID:    n.HostUUID,
				Engine:    engine,
				APIFamily: family,
				Strategy:  routing.StrategyDefault,
				Priority:  routing.DefaultPriority,
				Capacity:  0, // unbounded: a legacy node declares no admission limit
				Healthy:   nodeHealthy(n, healthy),
				Models:    []routing.ModelRouting{permissiveModel(model)},
			})
		}
	}
	return eps
}

// permissiveModels builds conservative ModelRouting for each inventory name.
func permissiveModels(inventory []string) []routing.ModelRouting {
	if len(inventory) == 0 {
		return nil
	}
	out := make([]routing.ModelRouting, 0, len(inventory))
	for _, m := range inventory {
		out = append(out, permissiveModel(m))
	}
	return out
}

// permissiveModel builds one conservative ModelRouting: the inventory name is the
// physical name, text and streaming are advertised, vision/tools are NOT invented,
// and context is left unknown (gate disabled).
func permissiveModel(name string) routing.ModelRouting {
	return routing.ModelRouting{
		Physical:     name,
		Capabilities: routing.Capabilities{Text: true, Streaming: true},
	}
}

func containsModel(inventory []string, model string) bool {
	for _, m := range inventory {
		if m == model {
			return true
		}
	}
	return false
}

func nodeHealthy(n noderec.DirectoryNode, healthy func(noderec.DirectoryNode) bool) bool {
	if healthy == nil {
		return true
	}
	return healthy(n)
}

// Router runs the shared routing pipeline for one inference surface, wired to a
// proxy's live state via injected providers. All providers read already-maintained
// control-plane state; Route performs no per-request control-plane I/O.
type Router struct {
	// EngineFilter narrows the engine set this surface routes across. Nil (the
	// default) considers every engine a node declares — the cross-engine case.
	EngineFilter EngineFilter
	// APIFamily is the wire contract of the surface the request arrived on. It
	// gates candidates whose declared family differs.
	APIFamily routing.APIFamily
	// Reserve configures the conservative context reserves used when classifying
	// (zero value = built-in defaults). It lets a deployment raise the output
	// reserve above 512 without editing the routing core.
	Reserve routing.ReservePolicy
	// Snapshot returns the current discovery nodes (cached control-plane state).
	Snapshot func() []noderec.DirectoryNode
	// Healthy reports whether a node's engine is currently healthy (defaults to
	// healthy when nil).
	Healthy func(noderec.DirectoryNode) bool
	// SchedulerOrder returns the scheduler's node ordering (node ids) for the
	// default strategy. May be nil.
	SchedulerOrder func() []string
	// Pools is the authoritative admission registry (nil = unbounded).
	Pools *routing.Pools
	// Transport / TransportFor perform the upstream round trip.
	Transport    http.RoundTripper
	TransportFor func(routing.Placement) http.RoundTripper
	// TargetFor resolves an EndpointID to its backend origin and node-local auth.
	TargetFor func(endpointID string) (routing.Target, bool)
	// Getenv resolves env-backed auth values (os.Getenv in production).
	Getenv func(string) string
	// Defaults supply timeout fallbacks.
	Defaults routing.TimeoutDefaults
	// IsInference marks inference paths (404 retryable).
	IsInference bool
	// RewriteModel rewrites the outbound body's model to the physical name.
	RewriteModel bool
	// MaxBody bounds the buffered request body (0 => DefaultMaxBody).
	MaxBody int64
}

// Route runs the pipeline for one request. Its outcomes are, in order:
//
//   - Oversize body (Content-Length or read exceeds the limit): 413, no upstream
//     contact, no truncation. handled=true.
//   - Body read error: 400, no upstream contact. handled=true.
//   - No enhanced node for the filtered engine set: handled=false, request body
//     restored byte-for-byte, so the caller uses its legacy path.
//   - Malformed JSON: 400 (a client error, never a routing 503). handled=true.
//   - Otherwise: classify, decide (eligibility before scheduling, strategy
//     resolved from the eligible set) and forward. handled=true.
func (rt *Router) Route(w http.ResponseWriter, r *http.Request) (handled bool, res routing.ForwardResult) {
	limit := rt.MaxBody
	if limit <= 0 {
		limit = DefaultMaxBody
	}

	// Oversize fast path: a declared Content-Length over the limit is rejected
	// without reading a single body byte.
	if r.ContentLength > limit {
		return true, writeClientError(w, http.StatusRequestEntityTooLarge, routing.ReasonRequestTooLarge, "request body exceeds the maximum size")
	}

	body, tooLarge, err := readLimited(r.Body, limit)
	if err != nil {
		// A read error is a client/transport problem, not a routing failure; never
		// forward or classify a partial body.
		return true, writeClientError(w, http.StatusBadRequest, routing.ReasonBadRequest, "could not read request body")
	}
	if tooLarge {
		return true, writeClientError(w, http.StatusRequestEntityTooLarge, routing.ReasonRequestTooLarge, "request body exceeds the maximum size")
	}

	var nodes []noderec.DirectoryNode
	if rt.Snapshot != nil {
		nodes = rt.Snapshot()
	}

	eps, hasMeta := EnhancedEndpoints(nodes, rt.EngineFilter, rt.Healthy)
	if !hasMeta {
		// Fully legacy cluster: hand back to the caller with the body intact so its
		// existing routing sees exactly what the client sent.
		restoreBody(r, body)
		return false, routing.ForwardResult{}
	}

	req, cerr := routing.ClassifyRequest(body, rt.Reserve)
	if errors.Is(cerr, routing.ErrMalformedBody) {
		return true, writeClientError(w, http.StatusBadRequest, routing.ReasonBadRequest, "request body is not valid JSON")
	}
	req.APIFamily = rt.APIFamily

	// Mixed-cluster: add conservative candidates for legacy nodes that advertise
	// the requested model but declared no routing metadata (Phase 6).
	eps = append(eps, LegacyCandidates(nodes, rt.EngineFilter, rt.Healthy, req.Model, rt.APIFamily)...)

	// Eligibility before scheduling; strategy resolved from the ELIGIBLE set only
	// so an ineligible endpoint cannot dictate ordering policy (Phase 8).
	var order []string
	if rt.SchedulerOrder != nil {
		order = rt.SchedulerOrder()
	}
	decision := routing.DecideResolved(req, eps, order)

	in := &routing.ForwardInput{
		Method:       r.Method,
		Path:         r.URL.RequestURI(),
		Header:       r.Header,
		Body:         body,
		Requirements: req,
		Ordered:      decision.Ordered,
		IsInference:  rt.IsInference,
		Pools:        rt.Pools,
		Transport:    rt.Transport,
		TransportFor: rt.TransportFor,
		Getenv:       rt.Getenv,
		Target:       rt.TargetFor,
		Defaults:     rt.Defaults,
		RewriteModel: rt.RewriteModel,
	}
	res = routing.Forward(r.Context(), w, in)
	return true, res
}

// readLimited reads up to limit+1 bytes so an oversized body is DETECTED rather
// than silently truncated: if the reader yields more than limit bytes, tooLarge
// is set and the (discarded) body must not be forwarded or classified. A nil
// reader is an empty body. A read error is returned verbatim.
func readLimited(r io.Reader, limit int64) (body []byte, tooLarge bool, err error) {
	if r == nil {
		return nil, false, nil
	}
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(b)) > limit {
		return nil, true, nil
	}
	return b, false, nil
}

// restoreBody re-wraps the buffered bytes so a legacy caller reads the exact
// request the client sent, and fixes ContentLength to match.
func restoreBody(r *http.Request, body []byte) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
}

// writeClientError writes a minimal JSON error (no request content, no secrets)
// and returns a ForwardResult recording the status and reason for diagnostics.
func writeClientError(w http.ResponseWriter, status int, reason routing.Reason, msg string) routing.ForwardResult {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": "routing_error"},
	})
	return routing.ForwardResult{
		StatusCode: status,
		Committed:  true,
		Rejections: []routing.Rejection{{Reason: reason}},
	}
}
