// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- fakes ------------------------------------------------------------------

type capturedReq struct {
	URLHost string
	Path    string
	Method  string
	Header  http.Header
	Body    string
}

type fakeRT struct {
	mu      sync.Mutex
	calls   []capturedReq
	respond func(req *http.Request, idx int) (*http.Response, error)
}

func newFakeRT(respond func(req *http.Request, idx int) (*http.Response, error)) *fakeRT {
	return &fakeRT{respond: respond}
}

func (f *fakeRT) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	f.mu.Lock()
	idx := len(f.calls)
	f.calls = append(f.calls, capturedReq{
		URLHost: req.URL.Host, Path: req.URL.Path, Method: req.Method,
		Header: req.Header.Clone(), Body: string(body),
	})
	f.mu.Unlock()
	return f.respond(req, idx)
}

func (f *fakeRT) count(host string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.URLHost == host {
			n++
		}
	}
	return n
}

func (f *fakeRT) lastFor(host string) (capturedReq, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].URLHost == host {
			return f.calls[i], true
		}
	}
	return capturedReq{}, false
}

func okResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// blockingReadBody blocks Read until Close is called (then returns EOF).
type blockingReadBody struct {
	once sync.Once
	done chan struct{}
}

func newBlockingBody() *blockingReadBody { return &blockingReadBody{done: make(chan struct{})} }
func (b *blockingReadBody) Read(p []byte) (int, error) {
	<-b.done
	return 0, io.EOF
}
func (b *blockingReadBody) Close() error {
	b.once.Do(func() { close(b.done) })
	return nil
}

// errAfterBody yields fixed chunks then a terminal error.
type errAfterBody struct {
	chunks [][]byte
	i      int
	err    error
}

func (b *errAfterBody) Read(p []byte) (int, error) {
	if b.i < len(b.chunks) {
		n := copy(p, b.chunks[b.i])
		b.i++
		return n, nil
	}
	return 0, b.err
}
func (b *errAfterBody) Close() error { return nil }

// chanBody yields chunks sent on ch; a closed ch is EOF.
type chanBody struct{ ch chan []byte }

func (b *chanBody) Read(p []byte) (int, error) {
	chunk, ok := <-b.ch
	if !ok {
		return 0, io.EOF
	}
	return copy(p, chunk), nil
}
func (b *chanBody) Close() error { return nil }

// signalRecorder closes wrote on the first body Write.
type signalRecorder struct {
	*httptest.ResponseRecorder
	once  sync.Once
	wrote chan struct{}
}

func (s *signalRecorder) Write(p []byte) (int, error) {
	s.once.Do(func() { close(s.wrote) })
	return s.ResponseRecorder.Write(p)
}

// --- helpers ----------------------------------------------------------------

func pl(id string, capacity int) Placement {
	return Placement{EndpointID: id, Engine: "e", Physical: "phys-" + id, APIFamily: APIFamilyOpenAI, Capacity: capacity}
}

func targetsFor(ids ...string) map[string]Target {
	m := make(map[string]Target, len(ids))
	for _, id := range ids {
		m[id] = Target{BaseURL: "http://" + id}
	}
	return m
}

func input(rt http.RoundTripper, ps *Pools, ordered []Placement, targets map[string]Target, opts ...func(*ForwardInput)) *ForwardInput {
	in := &ForwardInput{
		Method:      "POST",
		Path:        "/v1/chat/completions",
		Header:      http.Header{"Content-Type": []string{"application/json"}},
		Body:        []byte(`{"model":"logical"}`),
		Ordered:     ordered,
		IsInference: true,
		Pools:       ps,
		Transport:   rt,
		Target:      func(id string) (Target, bool) { t, ok := targets[id]; return t, ok },
		Defaults:    TimeoutDefaults{ResponseHeader: 5 * time.Second, FirstByte: 5 * time.Second},
	}
	for _, o := range opts {
		o(in)
	}
	return in
}

// forwardWithin runs Forward but fails the test if it does not return within a
// bound (guards against a hung test).
func forwardWithin(t *testing.T, d time.Duration, ctx context.Context, w http.ResponseWriter, in *ForwardInput) ForwardResult {
	t.Helper()
	ch := make(chan ForwardResult, 1)
	go func() { ch <- Forward(ctx, w, in) }()
	select {
	case r := <-ch:
		return r
	case <-time.After(d):
		t.Fatal("Forward did not return within bound")
		return ForwardResult{}
	}
}

// --- tests ------------------------------------------------------------------

func TestForward_NoEligible(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) { return okResp(200, "x"), nil })
	rec := httptest.NewRecorder()
	res := Forward(context.Background(), rec, input(rt, ps, nil, nil))
	if !res.NoEligible {
		t.Fatal("empty Ordered must report NoEligible")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if rt.count("A") != 0 || len(rt.calls) != 0 {
		t.Fatal("nothing should have been dialled")
	}
	if !strings.Contains(rec.Body.String(), "routing_error") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestForward_NormalText(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) { return okResp(200, "hello world"), nil })
	rec := httptest.NewRecorder()
	res := Forward(context.Background(), rec, input(rt, ps, []Placement{pl("A", 4)}, targetsFor("A")))
	if !res.Committed || res.ServedEndpoint != "A" || res.StatusCode != 200 {
		t.Fatalf("res = %+v", res)
	}
	if rec.Code != 200 || rec.Body.String() != "hello world" {
		t.Fatalf("recorder = %d %q", rec.Code, rec.Body.String())
	}
	if u, _ := ps.Used("A"); u != 0 {
		t.Fatalf("capacity leaked: used = %d", u)
	}
}

func TestForward_ModelRewriteToPhysical(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) { return okResp(200, "ok"), nil })
	in := input(rt, ps, []Placement{pl("A", 0)}, targetsFor("A"), func(in *ForwardInput) {
		in.Body = []byte(`{"model":"local-coding","temperature":0.5}`)
		in.RewriteModel = true
	})
	_ = Forward(context.Background(), httptest.NewRecorder(), in)
	got, ok := rt.lastFor("A")
	if !ok {
		t.Fatal("A was not dialled")
	}
	// The outbound body must carry the physical name, not the requested alias.
	if !strings.Contains(got.Body, `"model":"phys-A"`) {
		t.Fatalf("outbound body did not rewrite model to physical: %s", got.Body)
	}
	if strings.Contains(got.Body, "local-coding") {
		t.Fatalf("alias leaked into outbound body: %s", got.Body)
	}
	if !strings.Contains(got.Body, "0.5") {
		t.Fatalf("other fields lost in rewrite: %s", got.Body)
	}
}

func TestForward_AuthHeaderAppliedAndClientStripped(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) { return okResp(200, "ok"), nil })
	targets := map[string]Target{
		"A": {BaseURL: "http://A", Auth: LocalAuth{Headers: []HeaderSpec{{Name: "Authorization", ValueEnv: "BACKEND_TOKEN"}}}},
	}
	in := input(rt, ps, []Placement{pl("A", 0)}, targets, func(in *ForwardInput) {
		in.Header = http.Header{
			"Content-Type":  []string{"application/json"},
			"Authorization": []string{"Bearer CLIENT-SUPPLIED"}, // must be replaced
		}
		in.Getenv = fakeEnv(map[string]string{"BACKEND_TOKEN": "Bearer BACKEND-SECRET"})
	})
	_ = Forward(context.Background(), httptest.NewRecorder(), in)
	got, _ := rt.lastFor("A")
	if v := got.Header.Get("Authorization"); v != "Bearer BACKEND-SECRET" {
		t.Fatalf("upstream Authorization = %q, want backend secret", v)
	}
	if strings.Contains(got.Header.Get("Authorization"), "CLIENT-SUPPLIED") {
		t.Fatal("client-supplied credential leaked to backend")
	}
}

func TestForward_AuthUnavailableFailsOver(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) { return okResp(200, "ok"), nil })
	targets := map[string]Target{
		"A": {BaseURL: "http://A", Auth: LocalAuth{Headers: []HeaderSpec{{Name: "Authorization", ValueEnv: "MISSING"}}}},
		"B": {BaseURL: "http://B"},
	}
	in := input(rt, ps, []Placement{pl("A", 0), pl("B", 0)}, targets, func(in *ForwardInput) {
		in.Getenv = fakeEnv(nil) // MISSING unset -> A cannot auth
	})
	res := Forward(context.Background(), httptest.NewRecorder(), in)
	if res.ServedEndpoint != "B" {
		t.Fatalf("A should fail over on auth-unavailable; served %q", res.ServedEndpoint)
	}
	foundAuthRej := false
	for _, r := range res.Rejections {
		if r.EndpointID == "A" && r.Reason == ReasonAuthUnavailable {
			foundAuthRej = true
		}
	}
	if !foundAuthRej {
		t.Fatalf("expected AUTH_UNAVAILABLE rejection for A, got %+v", res.Rejections)
	}
	if u, _ := ps.Used("A"); u != 0 {
		t.Fatalf("A capacity leaked on auth failure: %d", u)
	}
}

func TestForward_CapacitySpillover(t *testing.T) {
	ps := NewPools()
	// Occupy A's single slot so the request must spill to B.
	holdA, ok := ps.Reserve("A", 1)
	if !ok {
		t.Fatal("pre-reserve A failed")
	}
	defer holdA()
	rt := newFakeRT(func(req *http.Request, _ int) (*http.Response, error) { return okResp(200, "ok"), nil })
	res := Forward(context.Background(), httptest.NewRecorder(),
		input(rt, ps, []Placement{pl("A", 1), pl("B", 2)}, targetsFor("A", "B")))
	if res.ServedEndpoint != "B" {
		t.Fatalf("served %q, want B", res.ServedEndpoint)
	}
	if rt.count("A") != 0 {
		t.Fatal("A must not be dialled when its capacity is full")
	}
	sawFull := false
	for _, r := range res.Rejections {
		if r.EndpointID == "A" && r.Reason == ReasonCapacityFull {
			sawFull = true
		}
	}
	if !sawFull {
		t.Fatalf("expected CAPACITY_FULL for A, got %+v", res.Rejections)
	}
}

func TestForward_CapacityFullEverywhere(t *testing.T) {
	ps := NewPools()
	hA, _ := ps.Reserve("A", 1)
	hB, _ := ps.Reserve("B", 1)
	defer hA()
	defer hB()
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) { return okResp(200, "ok"), nil })
	rec := httptest.NewRecorder()
	res := Forward(context.Background(), rec, input(rt, ps, []Placement{pl("A", 1), pl("B", 1)}, targetsFor("A", "B")))
	if len(rt.calls) != 0 {
		t.Fatal("nothing may be dialled when all capacity is full")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	full := 0
	for _, r := range res.Rejections {
		if r.Reason == ReasonCapacityFull {
			full++
		}
	}
	if full != 2 {
		t.Fatalf("want 2 CAPACITY_FULL, got %d (%+v)", full, res.Rejections)
	}
}

func TestForward_FailoverRetryableStatuses(t *testing.T) {
	for _, code := range []int{408, 429, 500, 502, 503, 504} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			ps := NewPools()
			rt := newFakeRT(func(req *http.Request, _ int) (*http.Response, error) {
				if req.URL.Host == "A" {
					return okResp(code, "err"), nil
				}
				return okResp(200, "good"), nil
			})
			rec := httptest.NewRecorder()
			res := Forward(context.Background(), rec, input(rt, ps, []Placement{pl("A", 0), pl("B", 0)}, targetsFor("A", "B")))
			if res.ServedEndpoint != "B" || rec.Body.String() != "good" {
				t.Fatalf("code %d: served %q body %q", code, res.ServedEndpoint, rec.Body.String())
			}
			if u, _ := ps.Used("A"); u != 0 {
				t.Fatalf("A capacity leaked after failover: %d", u)
			}
		})
	}
}

func TestForward_FailoverDialError(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(req *http.Request, _ int) (*http.Response, error) {
		if req.URL.Host == "A" {
			return nil, io.ErrUnexpectedEOF // transport/dial error
		}
		return okResp(200, "good"), nil
	})
	rec := httptest.NewRecorder()
	res := Forward(context.Background(), rec, input(rt, ps, []Placement{pl("A", 0), pl("B", 0)}, targetsFor("A", "B")))
	if res.ServedEndpoint != "B" {
		t.Fatalf("served %q, want B", res.ServedEndpoint)
	}
	if u, _ := ps.Used("A"); u != 0 {
		t.Fatalf("A capacity leaked after dial error: %d", u)
	}
}

func TestForward_NoFailoverOnClientErrors(t *testing.T) {
	for _, code := range []int{400, 401, 422} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			ps := NewPools()
			rt := newFakeRT(func(req *http.Request, _ int) (*http.Response, error) {
				return okResp(code, "client-error"), nil
			})
			rec := httptest.NewRecorder()
			res := Forward(context.Background(), rec, input(rt, ps, []Placement{pl("A", 0), pl("B", 0)}, targetsFor("A", "B")))
			if res.ServedEndpoint != "A" || res.StatusCode != code {
				t.Fatalf("code %d: served %q status %d", code, res.ServedEndpoint, res.StatusCode)
			}
			if rt.count("B") != 0 {
				t.Fatalf("code %d must not fail over to B", code)
			}
		})
	}
}

func TestForward_404FailoverOnlyForInference(t *testing.T) {
	// Inference: 404 is retryable (stale inventory).
	ps := NewPools()
	rt := newFakeRT(func(req *http.Request, _ int) (*http.Response, error) {
		if req.URL.Host == "A" {
			return okResp(404, "nf"), nil
		}
		return okResp(200, "good"), nil
	})
	res := Forward(context.Background(), httptest.NewRecorder(),
		input(rt, ps, []Placement{pl("A", 0), pl("B", 0)}, targetsFor("A", "B")))
	if res.ServedEndpoint != "B" {
		t.Fatalf("inference 404 should fail over; served %q", res.ServedEndpoint)
	}

	// Non-inference: 404 is terminal.
	ps2 := NewPools()
	rt2 := newFakeRT(func(req *http.Request, _ int) (*http.Response, error) { return okResp(404, "nf"), nil })
	res2 := Forward(context.Background(), httptest.NewRecorder(),
		input(rt2, ps2, []Placement{pl("A", 0), pl("B", 0)}, targetsFor("A", "B"), func(in *ForwardInput) { in.IsInference = false }))
	if res2.ServedEndpoint != "A" || rt2.count("B") != 0 {
		t.Fatalf("non-inference 404 must be terminal; served %q, B calls %d", res2.ServedEndpoint, rt2.count("B"))
	}
}

func TestForward_AllRetryableLastCommitted(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) { return okResp(503, "down"), nil })
	rec := httptest.NewRecorder()
	res := Forward(context.Background(), rec, input(rt, ps, []Placement{pl("A", 0), pl("B", 0)}, targetsFor("A", "B")))
	// Both 503; the last candidate's response is committed (nothing better).
	if !res.Committed || res.ServedEndpoint != "B" || rec.Code != 503 {
		t.Fatalf("res = %+v code=%d", res, rec.Code)
	}
	if u, _ := ps.Used("B"); u != 0 {
		t.Fatal("B capacity leaked")
	}
}

func TestForward_NoRetryAfterCommit(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(req *http.Request, _ int) (*http.Response, error) {
		if req.URL.Host == "A" {
			// 200, then first chunk commits, then the stream errors.
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{},
				Body:       &errAfterBody{chunks: [][]byte{[]byte("hello")}, err: io.ErrUnexpectedEOF},
			}, nil
		}
		return okResp(200, "SHOULD-NOT-BE-USED"), nil
	})
	rec := httptest.NewRecorder()
	res := Forward(context.Background(), rec, input(rt, ps, []Placement{pl("A", 0), pl("B", 0)}, targetsFor("A", "B")))
	if res.ServedEndpoint != "A" {
		t.Fatalf("served %q, want A (committed)", res.ServedEndpoint)
	}
	if rt.count("B") != 0 {
		t.Fatal("must not retry to B after the first byte was committed")
	}
	if !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("client should have received the committed bytes: %q", rec.Body.String())
	}
	if u, _ := ps.Used("A"); u != 0 {
		t.Fatal("A capacity leaked after stream error")
	}
}

func TestForward_ClientCancellationReleasesCapacity(t *testing.T) {
	ps := NewPools()
	started := make(chan struct{})
	rt := newFakeRT(func(req *http.Request, _ int) (*http.Response, error) {
		close(started)
		<-req.Context().Done() // block until cancelled
		return nil, req.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	done := make(chan ForwardResult, 1)
	go func() { done <- Forward(ctx, rec, input(rt, ps, []Placement{pl("A", 0), pl("B", 0)}, targetsFor("A", "B"))) }()
	<-started
	cancel()
	select {
	case res := <-done:
		if res.Committed {
			t.Fatal("cancelled request must not commit")
		}
		if rt.count("B") != 0 {
			t.Fatal("client cancellation must not fan out to B")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Forward hung after cancellation")
	}
	if u, _ := ps.Used("A"); u != 0 {
		t.Fatalf("A capacity leaked on cancellation: %d", u)
	}
}

func TestForward_PerEndpointResponseHeaderTimeout(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(req *http.Request, _ int) (*http.Response, error) {
		if req.URL.Host == "A" {
			<-req.Context().Done() // never returns headers until cancelled
			return nil, req.Context().Err()
		}
		return okResp(200, "good"), nil
	})
	// A has a tight 20ms response-header timeout; the generous default would be
	// 5s. If the per-endpoint value is honoured, failover to B is near-instant.
	a := pl("A", 0)
	a.Timeouts = Timeouts{ResponseHeaderMS: 20}
	rec := httptest.NewRecorder()
	res := forwardWithin(t, 3*time.Second, context.Background(), rec, input(rt, ps, []Placement{a, pl("B", 0)}, targetsFor("A", "B")))
	if res.ServedEndpoint != "B" {
		t.Fatalf("A's per-endpoint header timeout should trigger failover to B; served %q", res.ServedEndpoint)
	}
	if u, _ := ps.Used("A"); u != 0 {
		t.Fatalf("A capacity leaked after header timeout: %d", u)
	}
}

func TestForward_FirstByteTimeoutDistinctFromHeader(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(req *http.Request, _ int) (*http.Response, error) {
		if req.URL.Host == "A" {
			// Headers arrive immediately (200), but the first body byte never
			// comes: a first-byte timeout, NOT a response-header timeout.
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: newBlockingBody()}, nil
		}
		return okResp(200, "good"), nil
	})
	a := pl("A", 0)
	a.Timeouts = Timeouts{ResponseHeaderMS: 5000, FirstByteMS: 20}
	rec := httptest.NewRecorder()
	res := forwardWithin(t, 3*time.Second, context.Background(), rec, input(rt, ps, []Placement{a, pl("B", 0)}, targetsFor("A", "B")))
	if res.ServedEndpoint != "B" {
		t.Fatalf("first-byte timeout should fail over to B; served %q", res.ServedEndpoint)
	}
	if rec.Body.String() != "good" {
		t.Fatalf("client body = %q, want good", rec.Body.String())
	}
	if u, _ := ps.Used("A"); u != 0 {
		t.Fatalf("A capacity leaked after first-byte timeout: %d", u)
	}
}

func TestForward_StreamingReservationHeldThenReleased(t *testing.T) {
	ps := NewPools()
	body := &chanBody{ch: make(chan []byte, 4)}
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: body}, nil
	})
	rec := &signalRecorder{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{})}
	done := make(chan ForwardResult, 1)
	go func() {
		done <- Forward(context.Background(), rec, input(rt, ps, []Placement{pl("A", 2)}, targetsFor("A")))
	}()
	body.ch <- []byte("hello") // first chunk -> commit + write
	select {
	case <-rec.wrote:
	case <-time.After(3 * time.Second):
		t.Fatal("stream never committed")
	}
	if u, _ := ps.Used("A"); u != 1 {
		t.Fatalf("reservation must be held while streaming, used = %d", u)
	}
	body.ch <- []byte(" world")
	close(body.ch) // EOF
	select {
	case res := <-done:
		if !res.Committed || res.ServedEndpoint != "A" {
			t.Fatalf("res = %+v", res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream never finished")
	}
	if u, _ := ps.Used("A"); u != 0 {
		t.Fatalf("reservation must be released after stream, used = %d", u)
	}
	if got := rec.Body.String(); !strings.Contains(got, "hello") {
		t.Fatalf("client body = %q", got)
	}
}

func TestForward_MissingTargetFailsOver(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) { return okResp(200, "good"), nil })
	// Only B has a target; A resolves to no target and must be skipped.
	res := Forward(context.Background(), httptest.NewRecorder(),
		input(rt, ps, []Placement{pl("A", 0), pl("B", 0)}, targetsFor("B")))
	if res.ServedEndpoint != "B" {
		t.Fatalf("served %q, want B (A had no target)", res.ServedEndpoint)
	}
	if rt.count("A") != 0 {
		t.Fatal("A must not be dialled without a target")
	}
	if u, _ := ps.Used("A"); u != 0 {
		t.Fatalf("A capacity leaked when target missing: %d", u)
	}
}
