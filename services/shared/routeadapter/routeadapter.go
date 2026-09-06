// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package routeadapter wires the shared, engine-agnostic routing pipeline
// (routing.Classify -> routing.Decide -> routing.Pools -> routing.Forward) to a
// PAIR proxy's live control-plane state through small injected providers. Its
// purpose is to make a proxy's inference handler a thin call while keeping the
// whole decision-and-forward pipeline in one dependency-light place that is
// component-testable with fakes (a fake discovery snapshot, a fake scheduler
// order, a fake http.RoundTripper, an in-memory routing.Pools) and no socket,
// server, or engine.
//
// It also defines the single conversion from a discovery node to a
// routing.Endpoint, so every proxy builds candidates the same way, and it
// encodes the legacy seam: if NO candidate node advertises routing metadata for
// the engine, Route reports handled=false and the proxy falls back to its
// existing behaviour. Enhanced nodes (with a routing block) get the declared
// capability/priority/capacity contract enforced.
package routeadapter

import (
	"io"
	"net/http"

	"nvpair-shared/noderec"
	"nvpair-shared/routing"
)

// DefaultMaxBody bounds the buffered request body when a Router does not set one.
const DefaultMaxBody = 16 << 20

// Endpoints builds routing.Endpoints for one engine across the given discovery
// nodes, using each node's declared RoutingByEngine and the caller's health
// view. hasMetadata reports whether ANY node advertised routing metadata for the
// engine; when false the caller should use its existing (legacy) routing rather
// than treat every node as ineligible. The stable endpoint id is the node's
// hostUuid, matching the scheduler's ranking key so the default strategy aligns.
func Endpoints(nodes []noderec.DirectoryNode, engine string, healthy func(noderec.DirectoryNode) bool) (eps []routing.Endpoint, hasMetadata bool) {
	for _, n := range nodes {
		er, ok := n.EngineRouting(engine)
		if !ok {
			continue
		}
		hasMetadata = true
		h := true
		if healthy != nil {
			h = healthy(n)
		}
		eps = append(eps, er.Endpoint(n.HostUUID, engine, h))
	}
	return eps, hasMetadata
}

// Router runs the shared routing pipeline for one engine, wired to a proxy's
// live state via injected providers. All providers read already-maintained
// control-plane state; Route performs no per-request control-plane I/O.
type Router struct {
	// Engine is the (arbitrary) engine id this router serves.
	Engine string
	// APIFamily is the wire contract of the surface the request arrived on.
	APIFamily routing.APIFamily
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
	// TargetFor resolves an endpoint id to its backend origin and node-local auth.
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

// Route runs the pipeline for one request. It returns handled=false WITHOUT
// writing to w when no candidate node advertises routing metadata for this
// engine, so the caller falls back to its legacy path. Otherwise it classifies,
// decides (eligibility before scheduling), and forwards — or writes a local
// no-eligible/unavailable response when nothing is eligible or servable — and
// returns handled=true with the result.
func (rt *Router) Route(w http.ResponseWriter, r *http.Request) (handled bool, res routing.ForwardResult) {
	limit := rt.MaxBody
	if limit <= 0 {
		limit = DefaultMaxBody
	}
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(r.Body, limit))
	}

	var nodes []noderec.DirectoryNode
	if rt.Snapshot != nil {
		nodes = rt.Snapshot()
	}
	eps, hasMeta := Endpoints(nodes, rt.Engine, rt.Healthy)
	if !hasMeta {
		// No enhanced node for this engine: the caller uses its legacy routing.
		return false, routing.ForwardResult{}
	}

	req := routing.Classify(body)
	req.APIFamily = rt.APIFamily

	// Exactly one strategy applies. Explicit deterministic-priority intent (on any
	// eligible-set endpoint) dominates; otherwise the default scheduler ordering.
	strategy := routing.ResolveStrategy(eps)
	var order []string
	if strategy == routing.StrategyDefault && rt.SchedulerOrder != nil {
		order = rt.SchedulerOrder()
	}
	decision := routing.Decide(req, eps, strategy, order)

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
