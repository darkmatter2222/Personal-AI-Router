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

	// Execution-time reasons produced by the forwarder. They are deliberately
	// more precise than a single "unhealthy" bucket so a routing trace can tell,
	// for a heterogeneous fleet, a cold-model first-byte timeout apart from an
	// unreachable node.

	// ReasonTargetUnavailable: no backend target could be resolved for the
	// endpoint on this node (missing/removed local backend mapping).
	ReasonTargetUnavailable Reason = "TARGET_UNAVAILABLE"
	// ReasonConnectFailed: the transport failed before any response status line
	// (dial/TLS/connection error).
	ReasonConnectFailed Reason = "UPSTREAM_CONNECT_FAILED"
	// ReasonHeaderTimeout: the response status+headers did not arrive within the
	// endpoint's response-header timeout.
	ReasonHeaderTimeout Reason = "UPSTREAM_HEADER_TIMEOUT"
	// ReasonFirstByteTimeout: headers arrived but the first output byte did not,
	// within the endpoint's first-byte timeout (a cold model / stalled generation).
	ReasonFirstByteTimeout Reason = "UPSTREAM_FIRST_BYTE_TIMEOUT"
	// ReasonUpstream5xx: the upstream returned a retryable server error status.
	ReasonUpstream5xx Reason = "UPSTREAM_5XX"
	// ReasonClientCancelled: the client's context was cancelled before commit.
	ReasonClientCancelled Reason = "CLIENT_CANCELLED"
)

// String returns the reason code as a string.
func (r Reason) String() string { return string(r) }
