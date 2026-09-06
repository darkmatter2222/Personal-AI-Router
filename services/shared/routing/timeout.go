// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"time"

	"nvpair-shared/noderec"
)

// Timeout profile defaults. A per-endpoint declared value overrides only its
// own field: one slow endpoint's long first-byte budget must not globally
// weaken every other endpoint, and one fast endpoint's short budget must not
// clip a slow endpoint.
const (
	// DefaultConnectTimeout bounds dial + connect + TLS for upstream calls.
	DefaultConnectTimeout = 10 * time.Second
	// DefaultResponseHeaderTimeout bounds the wait for the upstream's status
	// line + headers. Response headers arriving is NOT necessarily first
	// inference output: for a streaming engine the headers precede the first
	// chunk by an unbounded amount, so the header budget and the first-byte
	// budget are distinct.
	DefaultResponseHeaderTimeout = 120 * time.Second
	// DefaultFirstByteTimeout bounds the wait for the first inference output
	// byte (first streamed chunk, or the response body when non-streaming).
	DefaultFirstByteTimeout = 120 * time.Second
)

// TimeoutProfile is the resolved per-endpoint timeout budget. All three
// fields are non-zero after resolution.
type TimeoutProfile struct {
	Connect        time.Duration
	ResponseHeader time.Duration
	FirstByte      time.Duration
}

// ResolveTimeouts resolves a declared per-endpoint timeout specification
// (milliseconds) against the defaults. A nil spec and any zero or negative
// field fall back to the default for that field, so "omitted" and "0" are
// unambiguously the default, never "no timeout".
func ResolveTimeouts(t *noderec.EngineTimeouts) TimeoutProfile {
	p := TimeoutProfile{
		Connect:        DefaultConnectTimeout,
		ResponseHeader: DefaultResponseHeaderTimeout,
		FirstByte:      DefaultFirstByteTimeout,
	}
	if t == nil {
		return p
	}
	if ms := positiveMS(t.ConnectMS); ms > 0 {
		p.Connect = time.Duration(ms) * time.Millisecond
	}
	if ms := positiveMS(t.ResponseHeaderMS); ms > 0 {
		p.ResponseHeader = time.Duration(ms) * time.Millisecond
	}
	if ms := positiveMS(t.FirstByteMS); ms > 0 {
		p.FirstByte = time.Duration(ms) * time.Millisecond
	}
	return p
}

func positiveMS(v int) int {
	if v > 0 {
		return v
	}
	return 0
}
