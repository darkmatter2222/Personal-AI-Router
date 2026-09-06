// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

// Reason is a stable, machine-readable code explaining why an endpoint was
// rejected for a request. Codes are stable identifiers safe to log, surface in
// diagnostics and assert on in tests. A routing explanation built from these
// never contains prompts, generated content, images, tool arguments or
// secrets.
type Reason string

const (
	// ReasonNone is the zero value: not rejected.
	ReasonNone Reason = ""
	// ReasonModelNotAvailable: the endpoint serves neither the requested
	// physical model name nor a declared logical alias for it.
	ReasonModelNotAvailable Reason = "MODEL_NOT_AVAILABLE"
	// ReasonAPIFamilyIncompatible: the endpoint speaks a different API family
	// than the request requires.
	ReasonAPIFamilyIncompatible Reason = "API_FAMILY_INCOMPATIBLE"
	// ReasonTextRequired: a text request reached a model that does not support
	// text.
	ReasonTextRequired Reason = "TEXT_REQUIRED"
	// ReasonVisionRequired: an image/multimodal request reached a model that does
	// not support vision.
	ReasonVisionRequired Reason = "VISION_REQUIRED"
	// ReasonToolsRequired: a tool-calling request reached a model that does not
	// support tools.
	ReasonToolsRequired Reason = "TOOLS_REQUIRED"
	// ReasonStreamingRequired: a streaming request reached a model that does not
	// support streaming.
	ReasonStreamingRequired Reason = "STREAMING_REQUIRED"
	// ReasonContextTooSmall: the endpoint's model context window is smaller than
	// the request's conservatively estimated required context.
	ReasonContextTooSmall Reason = "CONTEXT_TOO_SMALL"
	// ReasonEndpointDisabled: the endpoint is administratively disabled.
	ReasonEndpointDisabled Reason = "ENDPOINT_DISABLED"
	// ReasonEndpointUnhealthy: the endpoint is not currently healthy.
	ReasonEndpointUnhealthy Reason = "ENDPOINT_UNHEALTHY"
	// ReasonEndpointDraining: the endpoint is draining and accepts no new work.
	ReasonEndpointDraining Reason = "ENDPOINT_DRAINING"
	// ReasonCapacityFull: the endpoint's admission capacity is exhausted. This is
	// produced at reservation time by the executor, never by the pure Decide,
	// because live capacity is stateful.
	ReasonCapacityFull Reason = "CAPACITY_FULL"
	// ReasonAuthUnavailable: the endpoint requires local credentials that could
	// not be resolved on the owning node.
	ReasonAuthUnavailable Reason = "AUTH_UNAVAILABLE"
)

// String returns the reason code as a string.
func (r Reason) String() string { return string(r) }
