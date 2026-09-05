// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package routing is the control-plane routing core shared by the two
// inference proxies. It classifies an incoming request, applies capability
// eligibility gates BEFORE scheduling, then selects deterministically by
// declared priority and reserves static capacity. It operates on in-memory
// state only: no synchronous health probe, GPU query, or model discovery on
// the inference hot path (control plane vs data plane).
package routing

import (
	"encoding/json"
	"math"
	"sort"
	"sync"
)

// Rejection reasons are stable, explainable diagnostic codes. A routing
// decision can answer "why was this backend rejected?" without exposing
// request content.
const (
	ReasonVisionRequired    = "VISION_REQUIRED"
	ReasonToolsRequired     = "TOOLS_REQUIRED"
	ReasonStreamingRequired = "STREAMING_REQUIRED"
	ReasonContextTooSmall   = "CONTEXT_TOO_SMALL"
	ReasonModelNotAvailable = "MODEL_NOT_AVAILABLE"
	ReasonEndpointUnhealthy = "ENDPOINT_UNHEALTHY"
	ReasonEndpointDisabled  = "ENDPOINT_DISABLED"
	ReasonEndpointDraining  = "ENDPOINT_DRAINING"
	ReasonCapacityFull      = "CAPACITY_FULL"
)

// Selected marks a candidate that won selection.
const Selected = "selected"

// Req is a classified request: enough of an OpenAI/Ollama body to establish
// eligibility. It never carries prompt or response content, only derived
// flags and token budgets.
type Req struct {
	Model             string
	HasImages         bool
	RequiresTools     bool
	RequiresStreaming bool
	InputTokens       int
	MaxOutput         int
	RequiredContext   int
}

// probe is the minimal decode target. It reads just the fields needed for
// classification without a full model-specific parse.
type probe struct {
	Model    string `json:"model"`
	Stream    bool   `json:"stream"`
	MaxTokens  int    `json:"max_tokens"`
	MaxOutput int    `json:"max_completion_tokens"`
	Tools     any    `json:"tools"`
	Prompt    string `json:"prompt"`
	Options   *struct {
		NumPredict int `json:"num_predict"`
	} `json:"options"`
	Messages []struct {
		Content any `json:"content"`
	} `json:"messages"`
}

// Classify derives request requirements from a raw request body. It parses
// just enough JSON to detect images, tools, streaming, and a conservative
// context budget — no tokenizer. The input-token estimate is chars/4.
func Classify(body []byte) Req {
	var p probe
	if len(body) > 0 {
		_ = json.Unmarshal(body, &p)
	}

	req := Req{
		Model:             p.Model,
		RequiresStreaming: p.Stream,
	}

	// Max output: prefer an explicit max_completion_tokens (OpenAI), then
	// max_tokens (OpenAI), then Ollama options.num_predict.
	if p.MaxOutput > 0 {
		req.MaxOutput = p.MaxOutput
	} else if p.MaxTokens > 0 {
		req.MaxOutput = p.MaxTokens
	} else if p.Options != nil && p.Options.NumPredict > 0 {
		req.MaxOutput = p.Options.NumPredict
	}

	// Tools present (non-empty array) means the request requires tool calling.
	if p.Tools != nil {
		if arr, ok := p.Tools.([]any); ok && len(arr) > 0 {
			req.RequiresTools = true
		}
	}

	// Images: scan message content for image blocks / image_url entries.
	req.HasImages = detectImage(p.Messages)

	// Conservative input token estimate from prompt length (chars/4).
	prompt := p.Prompt
	for _, m := range p.Messages {
		prompt += stringContent(m.Content)
	}
	req.InputTokens = len(prompt) / 4

	// Required context = input + requested output + a safety reserve. The
	// reserve is the larger of a fixed floor and a small percent of the
	// prompt, so a request is not routed to an endpoint whose max context
	// cannot fit it.
	reserve := 4096
	if pct := int(math.Round(float64(len(prompt)) * 0.05)); pct > reserve {
		reserve = pct
	}
	req.RequiredContext = req.InputTokens + req.MaxOutput + reserve
	return req
}

// stringContent flattens a message content (string or block array) into a
// string length source for the token estimate.
func stringContent(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		for _, part := range v {
			if m, ok := part.(map[string]any); ok {
				if s, ok := m["text"]; ok {
					if str, ok := s.(string); ok {
						v = append(v, str)
					}
				}
			}
		}
		return ""
	default:
		return ""
	}
}

// detectImage reports whether any message content carries an image (OpenAI
// content-array image blocks, or an image_url entry).
func detectImage(msgs []struct {
	Content any `json:"content"`
}) bool {
	for _, m := range msgs {
		switch c := m.Content.(type) {
		case string:
			// plain text content; no image.
		case []any:
			for _, part := range c {
				pm, ok := part.(map[string]any)
				if !ok {
					continue
				}
				typ, _ := pm["type"].(string)
				if typ == "image" || typ == "image_url" || typ == "image_base64" || typ == "input_image" {
					return true
				}
				if _, has := pm["image_url"]; has {
					return true
				}
			}
		}
	}
	return false
}

// EndpointCaps is the capability + envelope snapshot of one endpoint/engine
// used for eligibility gating. A nil capability flag means "not declared",
// which is treated as unsupported.
type EndpointCaps struct {
	Text      *bool
	Vision    *bool
	Tools     *bool
	Streaming *bool
	Reasoning *bool
	MaxContext int
}

// boolOn returns the effective value of a possibly-nil capability flag.
// A nil flag is treated as false (unsupported).
func boolOn(v *bool) bool { return v != nil && *v }

// CheckCaps returns the first failing capability gate (a reason code) or ""
// when the endpoint can satisfy the request. Capability eligibility is
// checked BEFORE any priority/load scheduling.
func (c EndpointCaps) Check(req Req) string {
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
// A capacity <= 0 means unbounded (no admission limit).
func NewPool(capacity int) *Pool {
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

// Release frees a slot (no-op if already zero).
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

// Candidate is one routable endpoint with its eligibility inputs.
type Candidate struct {
	ID       string
	Priority  int
	Capacity   *Pool
	Healthy    bool
	Enabled    bool
	Draining   bool
	MaxContext int
	Caps       EndpointCaps
}

// Outcome records the selection result: the chosen candidate and a per-
// candidate explanation (reason code or "selected").
type Outcome struct {
	SelectedID string
	Reasons    map[string]string
}

// Select filters candidates through eligibility (lifecycle + capability
// gates) then, among the eligible set, picks deterministically by declared
// priority (lower = preferred) and reserves its static capacity. Capacity
// reservation happens only after eligibility, so an ineligible endpoint can
// never win merely because it is idle. If the preferred eligible endpoint is
// full, selection spills to the next eligible one.
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
		if reason := c.Caps.Check(req); reason != "" {
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
			out.Reasons[rest.ID] = "not_selected"
		}
		break
	}
	return out
}
