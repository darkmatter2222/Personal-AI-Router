// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import "sort"

// Placement is one eligible endpoint in policy order, carrying everything the
// executor needs to forward to it: the stable id, the resolved physical model
// name to rewrite the request to, the API family, the admission capacity and
// the per-endpoint timeout profile.
type Placement struct {
	// EndpointID is the unique deployment identity: it keys the admission pool,
	// the deterministic tie-break and target resolution.
	EndpointID string
	// NodeID is the owning PAIR host identity: it is the default strategy's
	// ranking key, so two endpoints on the same node share a scheduler rank.
	NodeID    string
	Engine    string
	Physical  string
	APIFamily APIFamily
	Priority  int
	Capacity  int
	Timeouts  Timeouts
}

// Rejection records why one endpoint was excluded, using a stable reason code.
// It carries no request content.
type Rejection struct {
	EndpointID string
	Engine     string
	Reason     Reason
}

// Decision is the pure routing result: the eligible endpoints in the selected
// policy's order (best first) and the rejected endpoints with their reasons.
// It contains only ids, engine names, model names and reason codes — never
// prompts, generated content, images, tool arguments or secrets.
type Decision struct {
	// Strategy is the policy Decide actually applied.
	Strategy Strategy
	// Ordered lists eligible endpoints, most-preferred first.
	Ordered []Placement
	// Rejected lists excluded endpoints with their first-failing reason.
	Rejected []Rejection
}

// Selected reports the single most-preferred eligible endpoint, if any. It is a
// convenience over Ordered[0].
func (d Decision) Selected() (Placement, bool) {
	if len(d.Ordered) == 0 {
		return Placement{}, false
	}
	return d.Ordered[0], true
}

// ResolveStrategy chooses the single policy to apply to a set of endpoints.
// Explicit operator intent dominates: if ANY endpoint requests
// deterministic-priority, the whole decision uses deterministic-priority;
// otherwise it uses the default (scheduler) ordering. This is a single,
// deterministic rule — the two strategies never run in sequence and never
// reorder one another.
//
// Callers MUST resolve strategy from the ELIGIBLE endpoints only (see
// DecideResolved). Resolving over the full input would let an endpoint that
// cannot serve the request dictate the ordering policy for the ones that can.
func ResolveStrategy(endpoints []Endpoint) Strategy {
	for _, e := range endpoints {
		if e.Strategy == StrategyDeterministicPriority {
			return StrategyDeterministicPriority
		}
	}
	return StrategyDefault
}

// DecideResolved runs the full routing pipeline with the strategy resolved from
// the ELIGIBLE endpoints only, then orders. This is the entry point the proxy
// route-adapter uses.
//
// The two-pass shape is deliberate and fixes a real ordering bug (Phase 8): an
// endpoint that declares StrategyDeterministicPriority but cannot serve the
// request (wrong model, missing capability, unhealthy, draining, over context)
// must NOT force the eligible endpoints into deterministic ordering. So this
// function evaluates eligibility first, resolves the strategy from the survivors
// only, and delegates ordering to Decide. Eligibility is evaluated twice (here
// and inside Decide); Evaluate is a pure, allocation-light function and the
// candidate count is small, so the clarity is worth the second pass.
//
// When eligible endpoints disagree on strategy the "deterministic dominates"
// rule of ResolveStrategy applies. That is the documented contract for a mixed
// eligible set; operators who want to forbid mixed policy within one logical
// routing group should reject it at configuration time (see Validate), not rely
// on ordering to paper over an inconsistent contract.
func DecideResolved(req Requirements, endpoints []Endpoint, defaultOrder []string) Decision {
	eligible := make([]Endpoint, 0, len(endpoints))
	for _, e := range endpoints {
		if Evaluate(req, e).OK {
			eligible = append(eligible, e)
		}
	}
	return Decide(req, endpoints, ResolveStrategy(eligible), defaultOrder)
}

// Decide evaluates every endpoint's eligibility, then orders the survivors by
// the given strategy. It is a pure function of its inputs: the same inputs
// always produce the same Decision, and it neither reserves capacity nor
// performs any I/O. This is the whole point of the CLASSIFY -> DECIDE ->
// RESERVE -> FORWARD split: the decision is exhaustively testable in memory.
//
// strategy selects ordering:
//   - StrategyDeterministicPriority: sort by Priority ascending (lower =
//     preferred), then by EndpointID ascending for a stable tie-break.
//   - StrategyDefault: sort by the endpoint's position in defaultOrder (the
//     scheduler's ranking of node ids), endpoints absent from defaultOrder
//     last, then by EndpointID ascending.
//
// For the default strategy, EndpointID must be the same key the scheduler
// ranks by (the node's hostUUID in PAIR), so the two align.
//
// The cardinal invariant holds here: ineligible endpoints are removed BEFORE
// ordering, so no scheduler preference or idle endpoint can resurrect a
// candidate that cannot satisfy the request.
func Decide(req Requirements, endpoints []Endpoint, strategy Strategy, defaultOrder []string) Decision {
	d := Decision{Strategy: strategy}
	for _, e := range endpoints {
		el := Evaluate(req, e)
		if !el.OK {
			d.Rejected = append(d.Rejected, Rejection{
				EndpointID: e.ID,
				Engine:     e.Engine,
				Reason:     el.Reason,
			})
			continue
		}
		d.Ordered = append(d.Ordered, Placement{
			EndpointID: e.ID,
			NodeID:     e.NodeID,
			Engine:     e.Engine,
			Physical:   el.Model.Physical,
			APIFamily:  e.APIFamily,
			Priority:   e.Priority,
			Capacity:   e.Capacity,
			Timeouts:   e.Timeouts,
		})
	}

	switch strategy {
	case StrategyDeterministicPriority:
		sort.SliceStable(d.Ordered, func(i, j int) bool {
			if d.Ordered[i].Priority != d.Ordered[j].Priority {
				return d.Ordered[i].Priority < d.Ordered[j].Priority
			}
			return d.Ordered[i].EndpointID < d.Ordered[j].EndpointID
		})
	default:
		rank := make(map[string]int, len(defaultOrder))
		for i, id := range defaultOrder {
			// First occurrence wins if the caller passed duplicates.
			if _, seen := rank[id]; !seen {
				rank[id] = i
			}
		}
		const absent = int(^uint(0) >> 1) // max int: unranked endpoints sort last
		rankOf := func(id string) int {
			if r, ok := rank[id]; ok {
				return r
			}
			return absent
		}
		// The default strategy ranks by NODE identity: the scheduler orders nodes,
		// so two endpoints hosted on one node share a rank and the EndpointID
		// tie-break then orders them. NodeID falls back to EndpointID when unset,
		// which preserves single-endpoint (node == endpoint) callers.
		rankKey := func(p Placement) string {
			if p.NodeID != "" {
				return p.NodeID
			}
			return p.EndpointID
		}
		sort.SliceStable(d.Ordered, func(i, j int) bool {
			ri, rj := rankOf(rankKey(d.Ordered[i])), rankOf(rankKey(d.Ordered[j]))
			if ri != rj {
				return ri < rj
			}
			return d.Ordered[i].EndpointID < d.Ordered[j].EndpointID
		})
	}
	return d
}
