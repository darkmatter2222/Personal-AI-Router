// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// streamThenBlockBody yields one chunk, then blocks Read until Close (or EOF).
type streamThenBlockBody struct {
	mu    sync.Mutex
	first bool
	done  chan struct{}
	once  sync.Once
}

func (b *streamThenBlockBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if !b.first {
		b.first = true
		b.mu.Unlock()
		return copy(p, []byte("hello")), nil
	}
	b.mu.Unlock()
	<-b.done
	return 0, io.EOF
}
func (b *streamThenBlockBody) Close() error {
	b.once.Do(func() { close(b.done) })
	return nil
}

// TestForward_FinalCandidateFirstByteTimeoutIsLocal504 is the Phase-3 fix: a
// final candidate that returns 200 headers but no body byte must NOT be
// committed as a 200-with-empty-body; it becomes a local 504.
func TestForward_FinalCandidateFirstByteTimeoutIsLocal504(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: newBlockingBody()}, nil
	})
	a := pl("A", 0)
	a.Timeouts = Timeouts{ResponseHeaderMS: 5000, FirstByteMS: 20}
	rec := httptest.NewRecorder()
	res := forwardWithin(t, 3*time.Second, context.Background(), rec, input(rt, ps, []Placement{a}, targetsFor("A")))
	if res.ServedEndpoint != "" {
		t.Fatalf("no upstream response should be committed, served %q", res.ServedEndpoint)
	}
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("final first-byte timeout must be a local 504, got %d", rec.Code)
	}
	if u, _ := ps.Used("A"); u != 0 {
		t.Fatalf("capacity leaked on final first-byte timeout: %d", u)
	}
}

func TestForward_AllFirstByteTimeoutIsLocal504(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: newBlockingBody()}, nil
	})
	a := pl("A", 0)
	a.Timeouts = Timeouts{FirstByteMS: 20}
	b := pl("B", 0)
	b.Timeouts = Timeouts{FirstByteMS: 20}
	rec := httptest.NewRecorder()
	res := forwardWithin(t, 3*time.Second, context.Background(), rec, input(rt, ps, []Placement{a, b}, targetsFor("A", "B")))
	if res.ServedEndpoint != "" || rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("both first-byte timeout should be local 504: served %q code %d", res.ServedEndpoint, rec.Code)
	}
	if u, _ := ps.Used("A"); u != 0 {
		t.Fatal("A leaked")
	}
	if u, _ := ps.Used("B"); u != 0 {
		t.Fatal("B leaked")
	}
}

func TestForward_NilPoolsIsUnbounded(t *testing.T) {
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) { return okResp(200, "ok"), nil })
	in := input(rt, nil, []Placement{pl("A", 0)}, targetsFor("A"))
	in.Pools = nil
	rec := httptest.NewRecorder()
	res := Forward(context.Background(), rec, in)
	if !res.Committed || res.ServedEndpoint != "A" || rec.Body.String() != "ok" {
		t.Fatalf("nil Pools should behave as unbounded and serve: %+v body=%q", res, rec.Body.String())
	}
}

func TestForward_NilTransportIsLocal502(t *testing.T) {
	ps := NewPools()
	in := input(nil, ps, []Placement{pl("A", 0)}, targetsFor("A"))
	in.Transport = nil
	rec := httptest.NewRecorder()
	res := Forward(context.Background(), rec, in)
	if rec.Code != http.StatusBadGateway || res.StatusCode != http.StatusBadGateway {
		t.Fatalf("nil Transport should be a local 502, got %d", rec.Code)
	}
}

func TestForward_NilResponseBodyCommitsHeaders(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: nil}, nil
	})
	rec := httptest.NewRecorder()
	res := Forward(context.Background(), rec, input(rt, ps, []Placement{pl("A", 0)}, targetsFor("A")))
	if !res.Committed || res.StatusCode != 200 {
		t.Fatalf("nil body should commit headers without panic: %+v", res)
	}
	if u, _ := ps.Used("A"); u != 0 {
		t.Fatal("capacity leaked with nil body")
	}
}

func TestForward_NilTargetResolverSkips(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) { return okResp(200, "ok"), nil })
	in := input(rt, ps, []Placement{pl("A", 0)}, nil)
	in.Target = nil // no resolver at all
	rec := httptest.NewRecorder()
	res := Forward(context.Background(), rec, in)
	if len(rt.calls) != 0 {
		t.Fatal("nothing should be dialled without a target resolver")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no-target should yield local 503, got %d", rec.Code)
	}
	if res.ServedEndpoint != "" {
		t.Fatal("no endpoint served")
	}
}

// TestForward_StreamCancellationReleasesCapacity: the client's context is
// cancelled mid-stream; Forward must return and release capacity even though the
// body would otherwise block.
func TestForward_StreamCancellationReleasesCapacity(t *testing.T) {
	ps := NewPools()
	body := &streamThenBlockBody{done: make(chan struct{})}
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: body}, nil
	})
	rec := &signalRecorder{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan ForwardResult, 1)
	go func() {
		done <- Forward(ctx, rec, input(rt, ps, []Placement{pl("A", 2)}, targetsFor("A")))
	}()
	select {
	case <-rec.wrote:
	case <-time.After(3 * time.Second):
		t.Fatal("stream never committed its first chunk")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Forward hung after mid-stream cancellation")
	}
	if u, _ := ps.Used("A"); u != 0 {
		t.Fatalf("capacity leaked on stream cancellation: %d", u)
	}
}

// TestForward_NonTransient5xxTerminal: 501/505 are not retried; the first
// candidate's response is committed and no failover occurs.
func TestForward_NonTransient5xxTerminal(t *testing.T) {
	for _, code := range []int{http.StatusNotImplemented, http.StatusHTTPVersionNotSupported} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			ps := NewPools()
			rt := newFakeRT(func(*http.Request, int) (*http.Response, error) { return okResp(code, "x"), nil })
			rec := httptest.NewRecorder()
			res := Forward(context.Background(), rec, input(rt, ps, []Placement{pl("A", 0), pl("B", 0)}, targetsFor("A", "B")))
			if res.ServedEndpoint != "A" || res.StatusCode != code {
				t.Fatalf("code %d should be terminal on A: served %q status %d", code, res.ServedEndpoint, res.StatusCode)
			}
			if rt.count("B") != 0 {
				t.Fatalf("code %d must not fail over to B", code)
			}
		})
	}
}

// TestForward_PerEndpointTransportSelected: TransportFor routes each endpoint to
// its own transport, so per-endpoint transport tuning (e.g. connect timeout) is
// isolated. A's transport returns a retryable 503; B's returns 200.
func TestForward_PerEndpointTransportSelected(t *testing.T) {
	ps := NewPools()
	rtA := newFakeRT(func(*http.Request, int) (*http.Response, error) { return okResp(503, "a-down"), nil })
	rtB := newFakeRT(func(*http.Request, int) (*http.Response, error) { return okResp(200, "b-ok"), nil })
	in := input(nil, ps, []Placement{pl("A", 0), pl("B", 0)}, targetsFor("A", "B"), func(in *ForwardInput) {
		in.Transport = nil
		in.TransportFor = func(p Placement) http.RoundTripper {
			if p.EndpointID == "A" {
				return rtA
			}
			return rtB
		}
	})
	rec := httptest.NewRecorder()
	res := Forward(context.Background(), rec, in)
	if res.ServedEndpoint != "B" || rec.Body.String() != "b-ok" {
		t.Fatalf("per-endpoint transport: served %q body %q", res.ServedEndpoint, rec.Body.String())
	}
	if rtA.count("A") != 1 {
		t.Fatalf("A should be dialled via its own transport once, got %d", rtA.count("A"))
	}
	if rtB.count("B") != 1 {
		t.Fatalf("B should be dialled via its own transport once, got %d", rtB.count("B"))
	}
}

// TestForward_ConnectionNominatedHeaderStripped: a header named in the request's
// Connection header is stripped before forwarding (hop-by-hop hardening).
func TestForward_ConnectionNominatedHeaderStripped(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) { return okResp(200, "ok"), nil })
	in := input(rt, ps, []Placement{pl("A", 0)}, targetsFor("A"), func(in *ForwardInput) {
		in.Header = http.Header{
			"Connection":    []string{"X-Secret-Hop, X-Another"},
			"X-Secret-Hop":  []string{"leak"},
			"X-Another":     []string{"leak2"},
			"X-Keep":        []string{"ok"},
			"Content-Type":  []string{"application/json"},
		}
	})
	_ = Forward(context.Background(), httptest.NewRecorder(), in)
	got, _ := rt.lastFor("A")
	if got.Header.Get("X-Secret-Hop") != "" || got.Header.Get("X-Another") != "" {
		t.Fatalf("Connection-nominated headers must be stripped: %v", got.Header)
	}
	if got.Header.Get("Connection") != "" {
		t.Fatal("Connection header itself must be stripped")
	}
	if got.Header.Get("X-Keep") != "ok" {
		t.Fatal("unrelated header must be preserved")
	}
}
