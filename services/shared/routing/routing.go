// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package routing is the control-plane routing core shared by the inference
// proxies. It is the pure DECISION layer of the inference pipeline:
//
//	CLASSIFY -> DECIDE -> RESERVE -> FORWARD
//
// Classify derives routing requirements from a raw request body. Decide
// (Select) applies capability and lifecycle eligibility gates BEFORE any
// priority or load scheduling, then selects deterministically by the routing
// strategy. The pools provide atomic admission reservations. The proxy owns
// the FORWARD phase and releases the reservation on every terminal path.
//
// It operates on in-memory state only: no synchronous health probe, GPU
// query, or model discovery on the inference hot path (control plane vs data
// plane).
package routing

import (
	"sort"
	"sync"

	"nvpair-shared/noderec"
)

// Rejection reasons are stable, explainable diagnostic codes. A routing
// decision can answer "why was this backend rejected?" without exposing
// request content.
const (
	ReasonTextRequired      = "TEXT_REQUIRED"
	ReasonVisionRequired    = "VISION_REQUIRED"
	ReasonToolsRequired     = "TOOLS_REQUIRED"
	ReasonStreamingRequired = "STREAMING_REQUIRED"
	ReasonContextTooSmall   = "CONTEXT_TOO_SMALL"
	ReasonModelNotAvailable = "MODEL_NOT_AVAILABLE"
	ReasonAPIFamily         = "API_FAMILY_INCOMPATIBLE"
	ReasonEndpointUnhealthy = "ENDPOINT_UNHEALTHY"
	ReasonEndpointDisabled  = "ENDPOINT_DISABLED"
	ReasonEndpointDraining  = "ENDPOINT_DRAINING"
	ReasonCapacityFull      = "CAPACITY_FULL"
	// ReasonNotSelected marks an eligible candidate that lost the selection
	// to a higher-priority eligible candidate (deterministic spillover).
	ReasonNotSelected = "NOT_SELECTED"
)

// Selected marks a candidate that won selection.
const Selected = "selected"

// Routing strategies. A request's candidate set is ordered by exactly ONE
// strategy, so two policies never run sequentially and reorder each other:
//
//   - StrategyDefault preserves PAIR's existing behavior: the legacy dynamic
//     scheduler (least-loaded ordering, reserveCandidate) owns the order, and
//     capability eligibility still runs first.
//   - StrategyDeterministic ranks purely by declared priority (lower =
//     preferred, ID tie-break) and bypasses the dynamic scheduler: the
//     selected candidate is tried first and the dynamic scheduler must not
//     re-order the list behind it.
const (
	StrategyDefault       = "scheduler"
	StrategyDeterministic = "deterministic"
)

// ValidStrategy reports whether s is a known routing strategy; an unknown or
// empty value falls back to StrategyDefault.
func ValidStrategy(s string) bool {
	return s == StrategyDefault || s == StrategyDeterministic
}

// EndpointCaps is the capability + envelope snapshot of one endpoint/engine
// used for eligibility gating. A nil capability flag means "not declared",
// which is treated as unsupported.
type EndpointCaps struct {
	Text      *bool
	Vision    *bool
	Tools     *bool
	Streaming *bool
	// Reasoning is metadata-only at the selection layer: it is carried and
	// advertised so a future request type can gate on it, but Select does not
	// reject on it today (no request form reliably declares a reasoning
	// requirement). It must not be presented as an enforced gate.
	Reasoning  *bool
	MaxContext int
}

// boolOn returns the effective value of a possibly-nil capability flag.
// A nil flag is treated as false (unsupported).
func boolOn(v *bool) bool { return v != nil && *v }

// Check returns the first failing capability gate (a reason code) or ""
// when the endpoint can satisfy the request. Capability eligibility is
// checked BEFORE any priority/load scheduling.
func (c EndpointCaps) Check(req Req) string {
	if req.Text && !boolOn(c.Text) {
		return ReasonTextRequired
	}
	if req.HasImages && !boolOn(c.Vision) {
		return ReasonVisionRequired
	}
	if req.RequiresTools && !boolOn(c.Tools) {
		return ReasonToolsRequired
	}
	if req.RequiresStreaming && !boolOn(c.Streaming) {
		return ReasonStreamingRequired
	}
	if req.RequiredContext > 0 && c.MaxContext > 0 && req.RequiredContext > c.MaxContext {
		return ReasonContextTooSmall
	}
	return ""
}

// Pool is a concurrency-safe static admission limit. Reservations are
// atomic so concurrent requests cannot oversubscribe an endpoint beyond its
// configured capacity.
type Pool struct {
	mu     sync.Mutex
	cap    int
	active int
}

// NewPool creates a pool admitting at most capacity concurrent requests.
// A capacity <= 0 means unbounded (no admission limit): this is the legacy
// default for endpoints that declare no static capacity.
func NewPool(capacity int) *Pool {
	if capacity < 0 {
		capacity = 0
	}
	return &Pool{cap: capacity}
}

// Reserve atomically takes a slot; false when the pool is already full.
func (p *Pool) Reserve() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cap > 0 && p.active >= p.cap {
		return false
	}
	p.active++
	return true
}

// Release frees a slot. Releasing at zero is a no-op, so a double-release
// cannot drive the counter negative.
func (p *Pool) Release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active > 0 {
		p.active--
	}
}

// Active returns the current in-flight reservation count.
func (p *Pool) Active() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active
}

// Capacity returns the pool's current admission limit (0 = unbounded).
func (p *Pool) Capacity() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cap
}

// Resize atomically changes the pool's admission limit so a runtime
// reconfiguration (e.g. static_capacity 1 -> 4 or 4 -> 1) takes effect
// without a process restart. Existing reservations are preserved: when
// shrinking below the current active count, new reservations are blocked
// until the active count falls back to or below the new limit — in-flight
// requests keep their slots, and a burst cannot oversubscribe the endpoint.
func (p *Pool) Resize(capacity int) {
	if capacity < 0 {
		capacity = 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cap = capacity
}

// ServesModel reports whether an endpoint serves the requested model: the
// requested name must match a physical model in served or a declared logical
// alias. An empty model (the request names no model) is trivially served by
// every endpoint.
func ServesModel(served []string, aliases []string, model string) bool {
	if model == "" {
		return true
	}
	if containsString(served, model) {
		return true
	}
	return containsString(aliases, model)
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// ModelRefs collects an endpoint's model declarations: the singular ModelRef
// (fixed single-model engines) plus the Models list (multi-model engines such
// as Ollama / LM Studio).
func ModelRefs(r *noderec.EngineRouting) []noderec.EngineModelRef {
	if r == nil {
		return nil
	}
	var refs []noderec.EngineModelRef
	if r.ModelRef != nil {
		refs = append(refs, *r.ModelRef)
	}
	refs = append(refs, r.Models...)
	return refs
}

// AliasesOf collects the logical model aliases an endpoint declares, across
// the singular ModelRef and every Models entry.
func AliasesOf(r *noderec.EngineRouting) []string {
	refs := ModelRefs(r)
	var out []string
	for i := range refs {
		out = append(out, refs[i].Aliases...)
	}
	return out
}

// EffectiveCaps returns the capability flags and context budget that apply to
// the requested model: a per-model declaration overrides the endpoint
// defaults; a model with no per-model declaration inherits them. The first
// matching declaration wins (deterministic: refs are walked in ModelRef-first
// order).
func EffectiveCaps(endpoint EndpointCaps, refs []noderec.EngineModelRef, model string) EndpointCaps {
	out := endpoint
	if model == "" {
		return out
	}
	for i := range refs {
		if !ServesModel([]string{refs[i].PhysicalName}, refs[i].Aliases, model) {
			continue
		}
		if caps := refs[i].Caps; caps != nil {
			out.Text = caps.Text
			out.Vision = caps.Vision
			out.Tools = caps.Tools
			out.Streaming = caps.Streaming
			out.Reasoning = caps.Reasoning
		}
		if refs[i].ContextMaxTokens > 0 {
			out.MaxContext = refs[i].ContextMaxTokens
		}
		break
	}
	return out
}

// Candidate is one routable endpoint with its eligibility inputs.
type Candidate struct {
	ID       string
	Priority int
	Capacity *Pool
	Healthy  bool
	Enabled  bool
	Draining bool
	// APIFamily is the endpoint's wire protocol family ("openai", "ollama").
	// Empty means undeclared and always compatible: legacy endpoints and
	// endpoints inside a single-family proxy declare no family because it is
	// implied.
	APIFamily string
	// Strategy selects the ordering policy (StrategyDefault or
	// StrategyDeterministic); empty defaults to StrategyDefault.
	Strategy   string
	Caps       EndpointCaps
	MaxContext int
	// Served is the physical model names this endpoint serves (the node's
	// model inventory); Aliases are the endpoint's declared logical model
	// aliases. Together they back the model-availability gate: a request
	// naming a model the endpoint does not serve is rejected with
	// MODEL_NOT_AVAILABLE instead of falling through to an ineligible
	// backend.
	Served  []string
	Aliases []string
	// Models carries the endpoint's per-model declarations. When the
	// requested model matches a declaration, its capability flags and
	// context budget override the endpoint defaults (see EffectiveCaps).
	Models []noderec.EngineModelRef
}

// Outcome records the selection result: the chosen candidate and a per-
// candidate explanation (a reason code, "selected", or "not_selected").
type Outcome struct {
	SelectedID string
	Reasons    map[string]string
}

// Select is the pure decision function: given classified requirements and
// endpoint snapshots, it returns the selected endpoint and an explainable
// per-candidate account. Eligibility — lifecycle, model availability, API
// family, capability and context gates — runs BEFORE priority scheduling,
// and capacity reservation happens only after eligibility, so an ineligible
// endpoint can never win merely because it is idle. The selected candidate's
// pool is reserved atomically; when the preferred eligible endpoint is full,
// selection spills to the next eligible one. When nothing is eligible the
// result carries no SelectedID and the caller must treat that as an
// authoritative rejection (no fallback to an unfiltered candidate list).
func Select(cands []Candidate, req Req) Outcome {
	out := Outcome{Reasons: map[string]string{}}
	var eligible []Candidate
	for _, c := range cands {
		if !c.Enabled {
			out.Reasons[c.ID] = ReasonEndpointDisabled
			continue
		}
		if !c.Healthy {
			out.Reasons[c.ID] = ReasonEndpointUnhealthy
			continue
		}
		if c.Draining {
			out.Reasons[c.ID] = ReasonEndpointDraining
			continue
		}
		if req.Model != "" && !ServesModel(c.Served, c.Aliases, req.Model) {
			out.Reasons[c.ID] = ReasonModelNotAvailable
			continue
		}
		if !apiFamilyCompatible(c.APIFamily, req.APIFamily) {
			out.Reasons[c.ID] = ReasonAPIFamily
			continue
		}
		caps := EffectiveCaps(c.Caps, c.Models, req.Model)
		// A per-model declaration may set context without setting caps (or
		// vice versa): the endpoint default and the model override compose,
		// with the model's declared budget winning when it declares one.
		if caps.MaxContext == 0 && c.MaxContext > 0 {
			caps.MaxContext = c.MaxContext
		}
		if reason := caps.Check(req); reason != "" {
			out.Reasons[c.ID] = reason
			continue
		}
		eligible = append(eligible, c)
	}

	// Deterministic priority: sort eligible by Priority (lower first),
	// tie-break by ID for stability.
	sort.SliceStable(eligible, func(i, j int) bool {
		if eligible[i].Priority != eligible[j].Priority {
			return eligible[i].Priority < eligible[j].Priority
		}
		return eligible[i].ID < eligible[j].ID
	})

	for i, c := range eligible {
		if c.Capacity != nil && !c.Capacity.Reserve() {
			out.Reasons[c.ID] = ReasonCapacityFull
			continue
		}
		out.SelectedID = c.ID
		out.Reasons[c.ID] = Selected
		// Mark the remaining eligible-but-not-selected candidates so operators
		// can see the full decision (priority spillover).
		for _, rest := range eligible[i+1:] {
			out.Reasons[rest.ID] = ReasonNotSelected
		}
		break
	}
	return out
}

// apiFamilyCompatible reports whether a candidate's declared API family can
// serve a request for the given family. Either side empty means "not
// declared", which is compatible: inside a single-family proxy the family is
// implied, and a legacy endpoint may declare none.
func apiFamilyCompatible(candidate, requested string) bool {
	if candidate == "" || requested == "" {
		return true
	}
	return candidate == requested
}
