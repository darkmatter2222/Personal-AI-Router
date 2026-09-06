// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package routing is the engine-agnostic, capability-aware routing core shared
// by PAIR's inference proxies (and, for header/auth primitives, the engine
// manager). It exists so PAIR can reason about heterogeneous inference backends
// through a declared contract — engines, endpoints, models, API families,
// capabilities, context limits, admission capacity, routing policy and
// per-endpoint timeouts — rather than an ever-growing list of runtime brand
// names.
//
// The package is deliberately dependency-free (standard library only). That
// keeps the routing decision a pure, deterministic function of its inputs and
// makes the whole package exhaustively unit-testable without starting any
// service, opening any socket, or importing any engine-specific code.
//
// The pipeline the package implements, in order, is:
//
//	CLASSIFY   Classify(body)            -> Requirements   (what the request needs)
//	DECIDE     Decide(req, endpoints, …) -> Decision       (who is eligible, in policy order)
//	RESERVE    Pools.Reserve(id, cap)    -> release, ok    (authoritative admission)
//	FORWARD    Forward(ctx, w, in)       -> result         (execute against an injectable transport)
//
// The cardinal rule is ELIGIBILITY BEFORE SCHEDULING: an endpoint that cannot
// satisfy a request is never selected merely because it is idle, less loaded,
// or preferred by a scheduler. Decide filters on capability/model/context/state
// first and only then orders the survivors by the selected policy.
//
// Backward compatibility is explicit. An endpoint carrying no routing metadata
// is expressed as a zero-value EngineRouting, whose resolved defaults are the
// permissive/legacy choices (enabled, unbounded capacity, default timeouts,
// default strategy, lowest explicit priority). New gating behaviour therefore
// engages only for endpoints an operator has actually described.
package routing

import (
	"strings"
	"time"
)

// APIFamily is the wire/API contract an endpoint speaks, represented
// independently of the engine brand. Two different runtimes (say vLLM and
// llama.cpp) can share one family; routing keys on the family, never on the
// brand.
type APIFamily string

const (
	// APIFamilyUnknown is the zero value. An endpoint with an unknown family is
	// not gated on family compatibility (legacy/permissive).
	APIFamilyUnknown APIFamily = ""
	// APIFamilyOpenAI is the OpenAI-compatible chat/completions contract.
	APIFamilyOpenAI APIFamily = "openai"
	// APIFamilyOllama is the Ollama native contract.
	APIFamilyOllama APIFamily = "ollama"
)

// Known reports whether f is a recognised family. The unknown/zero value is
// deliberately accepted everywhere as "unspecified", so Known is used only by
// Validate to reject a garbage non-empty value.
func (f APIFamily) Known() bool {
	switch f {
	case APIFamilyUnknown, APIFamilyOpenAI, APIFamilyOllama:
		return true
	default:
		return false
	}
}

// LifecycleMode declares whether PAIR owns an engine's process lifecycle or
// merely adopts and observes an externally managed one.
type LifecycleMode string

const (
	// LifecycleManaged is the zero value: PAIR may install/start/stop/restart
	// the engine (subject to the engine's own manifest capabilities).
	LifecycleManaged LifecycleMode = ""
	// LifecycleExternal marks an adopt-only engine. PAIR observes, health-checks
	// and routes to it but must never mutate its lifecycle. Read-only operations
	// (status, health, model inventory, routing metadata) remain permitted.
	LifecycleExternal LifecycleMode = "external"
)

// External reports whether the mode forbids lifecycle mutation.
func (m LifecycleMode) External() bool { return m == LifecycleExternal }

// Known reports whether m is a recognised mode.
func (m LifecycleMode) Known() bool {
	switch m {
	case LifecycleManaged, LifecycleExternal:
		return true
	default:
		return false
	}
}

// Strategy names an explicit, self-contained routing policy. Strategies never
// run in sequence and never reorder one another: Decide applies exactly one.
type Strategy string

const (
	// StrategyDefault is the zero value and preserves PAIR's existing scheduler
	// ordering (the DefaultOrder passed to Decide) after eligibility filtering.
	StrategyDefault Strategy = ""
	// StrategyDeterministicPriority orders eligible endpoints strictly by their
	// configured priority (lower number preferred), then by stable endpoint id.
	// It ignores the scheduler's dynamic ordering entirely.
	StrategyDeterministicPriority Strategy = "deterministic-priority"
)

// Known reports whether s is a recognised strategy.
func (s Strategy) Known() bool {
	switch s {
	case StrategyDefault, StrategyDeterministicPriority:
		return true
	default:
		return false
	}
}

// Capabilities is a model's declared capability set. Text, Vision, Tools and
// Streaming are ENFORCED by Decide. Reasoning, Embedding and Audio are
// metadata-only: they are carried and validated but not yet gated on, so they
// are never advertised as active gating behaviour (see docs/HETEROGENEOUS_*).
type Capabilities struct {
	Text      bool `json:"text"`
	Vision    bool `json:"vision"`
	Tools     bool `json:"tools"`
	Streaming bool `json:"streaming"`

	// Metadata-only / deferred — declared but NOT enforced by Decide.
	Reasoning bool `json:"reasoning,omitempty"`
	Embedding bool `json:"embedding,omitempty"`
	Audio     bool `json:"audio,omitempty"`
}

// Context is a model's context-window contract.
type Context struct {
	// MaxTokens is the largest total (input + requested output) token count the
	// model can serve. Zero means "unknown/undeclared": context gating is
	// disabled for this model rather than treating it as a zero-size window.
	MaxTokens int `json:"maxTokens,omitempty"`
}

// ModelRouting is one model's routing contract on one endpoint: its physical
// (upstream) name, the logical aliases a client may request it by, its
// capabilities and its context window. Capabilities and context live at the
// model scope (not the engine scope) so a multi-model engine can serve models
// with genuinely different capabilities and context sizes.
type ModelRouting struct {
	// Physical is the model name the upstream backend actually serves and the
	// name PAIR rewrites an outbound request to. Required.
	Physical string `json:"physical"`
	// Aliases are stable logical names a client may request instead of Physical.
	// Alias equivalence is always explicit; it is never inferred from similar
	// names.
	Aliases      []string     `json:"aliases,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
	Context      Context      `json:"context"`
}

// Timeouts is a per-endpoint timeout profile in milliseconds. A zero field
// means "use the caller's default"; one endpoint's longer profile never
// weakens another's, because each is resolved against the default independently
// at the point the value is consumed.
type Timeouts struct {
	// ConnectMS bounds establishing the TCP/TLS connection.
	ConnectMS int `json:"connectMs,omitempty"`
	// ResponseHeaderMS bounds waiting for the upstream response status+headers.
	ResponseHeaderMS int `json:"responseHeaderMs,omitempty"`
	// FirstByteMS bounds waiting for the first inference OUTPUT byte after
	// headers arrive. This is distinct from ResponseHeaderMS: an OpenAI-style
	// backend can send 200 + headers immediately and only then spend seconds
	// producing the first token, so a cold large model needs a generous
	// FirstByteMS even with a tight ResponseHeaderMS.
	FirstByteMS int `json:"firstByteMs,omitempty"`
	// ActionMS bounds a manifest-declared engine-manager action (e.g. a long
	// model pull) and is unrelated to inference proxying.
	ActionMS int `json:"actionMs,omitempty"`
}

// Health is an endpoint's health-probe contract.
type Health struct {
	// Path is the HTTP path a health probe should GET (e.g. "/health"). Empty
	// leaves the probe policy to the caller.
	Path string `json:"path,omitempty"`
}

// EngineRouting is the declarative per-engine routing metadata an operator
// configures (in the engine manifest) and PAIR propagates on
// DirectoryNode.RoutingByEngine. Every field is optional; each absent field
// resolves to a backward-compatible default via the Resolved* helpers.
//
// EngineRouting NEVER carries credentials. Backend authentication is a
// node-local concern (see LocalAuth) resolved only on the owning node and is
// deliberately absent from this type so it can never enter discovery, routing
// metadata, scheduler data or a peer trust boundary.
type EngineRouting struct {
	// APIFamily is the wire contract this engine's endpoints speak.
	APIFamily APIFamily `json:"apiFamily,omitempty"`
	// Lifecycle declares managed vs external (adopt-only) ownership.
	Lifecycle LifecycleMode `json:"lifecycle,omitempty"`
	// Strategy selects the routing policy for this engine's endpoints.
	Strategy Strategy `json:"strategy,omitempty"`
	// Priority is the deterministic-priority preference (lower = preferred). It
	// is a pointer so "omitted" is distinguishable from an explicit 0: an
	// omitted priority resolves to DefaultPriority (lowest preference) so an
	// unconfigured endpoint never accidentally becomes the most-preferred one.
	Priority *int `json:"priority,omitempty"`
	// Capacity is the maximum number of concurrently admitted requests. Zero
	// means unbounded (the legacy default): no admission gate is applied.
	Capacity int `json:"capacity,omitempty"`
	// Disabled turns the endpoint off. The zero value (false) means ENABLED, so
	// an omitted field can never silently disable an endpoint — the safe default
	// is the one Go's zero value already gives.
	Disabled bool `json:"disabled,omitempty"`
	// Draining stops new admissions while letting in-flight work finish.
	Draining bool `json:"draining,omitempty"`
	// Timeouts is the per-endpoint timeout profile.
	Timeouts Timeouts `json:"timeouts,omitempty"`
	// Health is the health-probe contract.
	Health Health `json:"health,omitempty"`
	// Models is the per-model routing contract. An empty list means the engine
	// declares no model-scoped routing; callers then fall back to their existing
	// inventory-based ownership (legacy behaviour).
	Models []ModelRouting `json:"models,omitempty"`
}

// DefaultPriority is the resolved priority of an endpoint that declares none.
// It is large so an unconfigured endpoint sorts LAST under deterministic
// priority — never accidentally first.
const DefaultPriority = 1 << 30

// ResolvedPriority returns the configured priority, or DefaultPriority when
// unset.
func (e EngineRouting) ResolvedPriority() int {
	if e.Priority == nil {
		return DefaultPriority
	}
	return *e.Priority
}

// Enabled reports whether the endpoint accepts routing. It is the inverse of
// Disabled so the zero value is "enabled".
func (e EngineRouting) Enabled() bool { return !e.Disabled }

// ResolvedStrategy returns the configured strategy, or StrategyDefault.
func (e EngineRouting) ResolvedStrategy() Strategy {
	if e.Strategy == "" {
		return StrategyDefault
	}
	return e.Strategy
}

// External reports whether the engine is adopt-only (lifecycle mutation
// forbidden).
func (e EngineRouting) External() bool { return e.Lifecycle.External() }

// Endpoint is a fully resolved routing candidate: EngineRouting's declarative
// fields with defaults applied, plus the runtime Healthy facet the control
// plane maintains. Decide operates on a slice of these, which keeps it a pure
// function of already-materialised inputs.
type Endpoint struct {
	// ID is the EndpointID: a stable, unique identifier for one runtime
	// deployment. It keys the admission pool, the deterministic tie-break, and
	// target resolution. It is NOT the node identity; a single node hosting two
	// engines has two distinct endpoint IDs but one NodeID. Build it with
	// EndpointKey so the (node, engine) pair cannot collide.
	ID string
	// NodeID is the stable PAIR host identity that owns this endpoint. It is the
	// scheduler ranking key for the default strategy: two endpoints on the same
	// node share a NodeID and therefore a rank, while remaining independently
	// admissible via their distinct EndpointID. When empty, ranking falls back to
	// ID so legacy single-endpoint callers keep their behaviour.
	NodeID string
	// Engine is the (arbitrary) engine id this endpoint runs.
	Engine string
	// APIFamily is the wire contract; APIFamilyUnknown disables the family gate.
	APIFamily APIFamily
	// Strategy is the resolved routing policy this endpoint requests.
	Strategy Strategy
	// Priority is the resolved deterministic-priority preference.
	Priority int
	// Capacity is the resolved admission capacity; 0 = unbounded.
	Capacity int
	// Disabled/Draining/Healthy are the endpoint-state facets.
	Disabled bool
	Draining bool
	Healthy  bool
	// Timeouts is the resolved timeout profile.
	Timeouts Timeouts
	// Models is the per-model routing contract for this endpoint.
	Models []ModelRouting
}

// Endpoint builds a resolved Endpoint from this EngineRouting for the given
// stable id, engine id and runtime health. It applies every default so callers
// (and tests) get one materialisation path. The legacy form treats the endpoint
// as its own node (NodeID == ID), which preserves single-endpoint ranking
// behaviour. Heterogeneous callers that host several engines per node should use
// EndpointFor to keep NodeID and EndpointID distinct.
func (e EngineRouting) Endpoint(id, engine string, healthy bool) Endpoint {
	return e.EndpointFor(id, id, engine, healthy)
}

// EndpointFor builds a resolved Endpoint with an explicit NodeID (scheduler
// ranking key) and EndpointID (admission/tie-break/target key). This is the
// heterogeneous materialisation path: one node may produce several endpoints
// with a shared NodeID and distinct EndpointIDs (one per engine).
func (e EngineRouting) EndpointFor(nodeID, endpointID, engine string, healthy bool) Endpoint {
	return Endpoint{
		ID:        endpointID,
		NodeID:    nodeID,
		Engine:    engine,
		APIFamily: e.APIFamily,
		Strategy:  e.ResolvedStrategy(),
		Priority:  e.ResolvedPriority(),
		Capacity:  e.Capacity,
		Disabled:  e.Disabled,
		Draining:  e.Draining,
		Healthy:   healthy,
		Timeouts:  e.Timeouts,
		Models:    e.Models,
	}
}

// EndpointKey builds a collision-free EndpointID from a node identity and an
// engine identity. The two components are joined with a NUL byte, which cannot
// appear in a PAIR host UUID or in a validated engine id (engine ids match
// [A-Za-z0-9._-]+), so distinct (node, engine) pairs always produce distinct
// keys and no concatenation ambiguity is possible. A future third component
// (a per-node deployment ordinal) can be appended with the same separator.
func EndpointKey(parts ...string) string {
	return strings.Join(parts, "\x00")
}

// msOr converts a non-positive millisecond count to the fallback duration and a
// positive one to its duration. It is the single place timeout-default
// semantics (0 => default) are expressed.
func msOr(ms int, fallback time.Duration) time.Duration {
	if ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

// Connect returns the resolved connect timeout, falling back to d when unset.
func (t Timeouts) Connect(d time.Duration) time.Duration { return msOr(t.ConnectMS, d) }

// ResponseHeader returns the resolved response-header timeout.
func (t Timeouts) ResponseHeader(d time.Duration) time.Duration {
	return msOr(t.ResponseHeaderMS, d)
}

// FirstByte returns the resolved first-output-byte timeout.
func (t Timeouts) FirstByte(d time.Duration) time.Duration { return msOr(t.FirstByteMS, d) }

// Action returns the resolved action timeout.
func (t Timeouts) Action(d time.Duration) time.Duration { return msOr(t.ActionMS, d) }
