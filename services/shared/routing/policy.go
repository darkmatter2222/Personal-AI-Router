// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import "sort"

// Placement is one eligible endpoint in policy order, carrying everything the
// executor needs to forward to it: the stable id, the resolved physical model
// name to rewrite the request to, the API family, the admission capacity and
// the per-endpoint timeout profile.
type Placement struct {
	EndpointID string
	Engine     string
	Physical   string
	APIFamily  APIFamily
	Priority   int
	Capacity   int
	Timeouts   Timeouts
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
func ResolveStrategy(endpoints []Endpoint) Strategy {
	for _, e := range endpoints {
		if e.Strategy == StrategyDeterministicPriority {
			return StrategyDeterministicPriority
		}
	}
	return StrategyDefault
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
		sort.SliceStable(d.Ordered, func(i, j int) bool {
			ri, rj := rankOf(d.Ordered[i].EndpointID), rankOf(d.Ordered[j].EndpointID)
			if ri != rj {
				return ri < rj
			}
			return d.Ordered[i].EndpointID < d.Ordered[j].EndpointID
		})
	}
	return d
}
