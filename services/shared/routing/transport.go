// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// TransportProfile is the per-endpoint transport tuning derived from an
// endpoint's Timeouts. Two endpoints with different connect timeouts have
// different profiles and therefore different (cached) transports, so one slow
// endpoint's dial timeout never becomes another endpoint's.
type TransportProfile struct {
	// Connect bounds establishing the TCP/TLS connection (net.Dialer.Timeout).
	Connect time.Duration
	// ResponseHeader is set on the transport as defence-in-depth; the forwarder
	// also enforces a response-header deadline independently.
	ResponseHeader time.Duration
	// KeepAlive is the dialer keep-alive; zero uses a sensible default.
	KeepAlive time.Duration
}

// ProfileFor derives a TransportProfile from an endpoint's Timeouts, applying
// the given defaults for any unset (zero) field. This is where per-endpoint
// ConnectMS is actually consumed.
func ProfileFor(to Timeouts, connectDefault, headerDefault time.Duration) TransportProfile {
	return TransportProfile{
		Connect:        to.Connect(connectDefault),
		ResponseHeader: to.ResponseHeader(headerDefault),
	}
}

// dialerFor builds the net.Dialer for a profile. It is a named helper so a test
// can assert the connect timeout is actually threaded onto the dialer without a
// real network connection.
func dialerFor(p TransportProfile) *net.Dialer {
	ka := p.KeepAlive
	if ka <= 0 {
		ka = 30 * time.Second
	}
	return &net.Dialer{Timeout: p.Connect, KeepAlive: ka}
}

// TransportFactory builds and caches *http.Transport keyed by TransportProfile.
// It clones a shared template (idle-connection pool tuning) and applies the
// per-profile dial/response-header timeouts, so a per-endpoint transport is
// built at most once rather than on every request.
type TransportFactory struct {
	mu       sync.Mutex
	cache    map[TransportProfile]*http.Transport
	template *http.Transport
}

// NewTransportFactory returns a factory that clones template for each profile. A
// nil template uses a modest default pool. The template's TLS/proxy settings (if
// any) are preserved by Clone.
func NewTransportFactory(template *http.Transport) *TransportFactory {
	if template == nil {
		template = &http.Transport{
			MaxIdleConns:        50,
			MaxIdleConnsPerHost: 50,
			IdleConnTimeout:     90 * time.Second,
		}
	}
	return &TransportFactory{
		cache:    make(map[TransportProfile]*http.Transport),
		template: template,
	}
}

// For returns the transport for a profile, building and caching it on first use.
// The returned transport is shared; callers must not mutate it.
func (f *TransportFactory) For(p TransportProfile) *http.Transport {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.cache[p]; ok {
		return t
	}
	t := f.template.Clone()
	t.DialContext = dialerFor(p).DialContext
	if p.ResponseHeader > 0 {
		t.ResponseHeaderTimeout = p.ResponseHeader
	}
	f.cache[p] = t
	return t
}

// Len reports the number of distinct transports built (for observability/tests).
func (f *TransportFactory) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.cache)
}
