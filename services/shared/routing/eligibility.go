// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

// Eligibility is the result of evaluating one endpoint against one request's
// Requirements. When OK is true, Model carries the resolved model routing
// (including the physical name to rewrite the outbound request to). When OK is
// false, Reason carries the single, first-failing, stable rejection code.
type Eligibility struct {
	OK     bool
	Reason Reason
	// Model is the matched model routing. It is populated whenever the model
	// resolved (i.e. any reason other than MODEL_NOT_AVAILABLE), so a caller can
	// still see which model a capability/state rejection referred to.
	Model ModelRouting
}

// Evaluate applies the capability/model/context/state gates to one endpoint in
// the documented pipeline order and returns the first-failing reason, or an OK
// result carrying the resolved model. It intentionally does NOT consider
// admission capacity: capacity is live, stateful and authoritative only at
// reservation time, so ReasonCapacityFull is produced by the executor, never
// here.
//
// A request always requires the Text capability: every request shape this
// package classifies (chat, completions, generate) is a text-generation
// request, so a model that declares text=false is ineligible for it. Vision,
// Tools and Streaming are required only when the request actually needs them.
// Context is gated only when the model declares a non-zero MaxTokens (zero means
// "undeclared", which disables the gate rather than asserting a zero-size
// window).
func Evaluate(req Requirements, e Endpoint) Eligibility {
	// 1. Model availability (physical name or declared alias) — resolved first so
	//    the reason is never lost to an unrelated earlier filter.
	model, ok := MatchModel(e, req.Model)
	if !ok {
		return Eligibility{Reason: ReasonModelNotAvailable}
	}

	// 2. API family compatibility (only when both sides declare a known family).
	if req.APIFamily.Known() && req.APIFamily != APIFamilyUnknown &&
		e.APIFamily.Known() && e.APIFamily != APIFamilyUnknown &&
		req.APIFamily != e.APIFamily {
		return Eligibility{Reason: ReasonAPIFamilyIncompatible, Model: model}
	}

	// 3. Text (always required for a generation request).
	if !model.Capabilities.Text {
		return Eligibility{Reason: ReasonTextRequired, Model: model}
	}
	// 4. Vision.
	if req.HasImages && !model.Capabilities.Vision {
		return Eligibility{Reason: ReasonVisionRequired, Model: model}
	}
	// 5. Tools.
	if req.RequiresTools && !model.Capabilities.Tools {
		return Eligibility{Reason: ReasonToolsRequired, Model: model}
	}
	// 6. Streaming.
	if req.RequiresStream && !model.Capabilities.Streaming {
		return Eligibility{Reason: ReasonStreamingRequired, Model: model}
	}
	// 7. Context sufficiency (gated only when the model declares a window).
	if model.Context.MaxTokens > 0 && req.RequiredContext > model.Context.MaxTokens {
		return Eligibility{Reason: ReasonContextTooSmall, Model: model}
	}

	// 8-10. Endpoint state.
	if e.Disabled {
		return Eligibility{Reason: ReasonEndpointDisabled, Model: model}
	}
	if !e.Healthy {
		return Eligibility{Reason: ReasonEndpointUnhealthy, Model: model}
	}
	if e.Draining {
		return Eligibility{Reason: ReasonEndpointDraining, Model: model}
	}

	return Eligibility{OK: true, Model: model}
}
