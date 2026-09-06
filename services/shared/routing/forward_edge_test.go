// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestForward_StreamingMultipleChunksInOrder: all body chunks reach the client
// in order, and capacity is released after the stream completes.
func TestForward_StreamingMultipleChunksInOrder(t *testing.T) {
	ps := NewPools()
	body := &errAfterBody{chunks: [][]byte{[]byte("one "), []byte("two "), []byte("three")}, err: io.EOF}
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: body}, nil
	})
	rec := httptest.NewRecorder()
	res := Forward(context.Background(), rec, input(rt, ps, []Placement{pl("A", 0)}, targetsFor("A")))
	if !res.Committed || rec.Body.String() != "one two three" {
		t.Fatalf("streamed body = %q, res = %+v", rec.Body.String(), res)
	}
	if u, _ := ps.Used("A"); u != 0 {
		t.Fatal("capacity leaked after streaming")
	}
}

// TestForward_EmptyBodyCommits: a 200 with an empty body commits and releases.
func TestForward_EmptyBodyCommits(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	rec := httptest.NewRecorder()
	res := Forward(context.Background(), rec, input(rt, ps, []Placement{pl("A", 0)}, targetsFor("A")))
	if !res.Committed || res.StatusCode != 200 || rec.Body.Len() != 0 {
		t.Fatalf("empty body should commit 200: res=%+v body=%q", res, rec.Body.String())
	}
	if u, _ := ps.Used("A"); u != 0 {
		t.Fatal("capacity leaked on empty body")
	}
}

// TestForward_MethodAndPathPassthrough: the request method and path reach the
// upstream unchanged.
func TestForward_MethodAndPathPassthrough(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) { return okResp(200, "ok"), nil })
	in := input(rt, ps, []Placement{pl("A", 0)}, targetsFor("A"), func(in *ForwardInput) {
		in.Method = "POST"
		in.Path = "/v1/embeddings"
	})
	_ = Forward(context.Background(), httptest.NewRecorder(), in)
	got, ok := rt.lastFor("A")
	if !ok || got.Method != "POST" || got.Path != "/v1/embeddings" {
		t.Fatalf("method/path not passed through: %q %q (found=%v)", got.Method, got.Path, ok)
	}
}

// TestForward_RewriteDisabledLeavesBody: with RewriteModel off, the outbound
// body is byte-identical even when a physical name is available.
func TestForward_RewriteDisabledLeavesBody(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(*http.Request, int) (*http.Response, error) { return okResp(200, "ok"), nil })
	in := input(rt, ps, []Placement{pl("A", 0)}, targetsFor("A"), func(in *ForwardInput) {
		in.Body = []byte(`{"model":"logical"}`)
		in.RewriteModel = false
	})
	_ = Forward(context.Background(), httptest.NewRecorder(), in)
	got, _ := rt.lastFor("A")
	if !strings.Contains(got.Body, `"model":"logical"`) {
		t.Fatalf("body must be untouched when RewriteModel=false: %s", got.Body)
	}
}

// TestForward_HeaderTimeoutAllCandidates: when every candidate times out waiting
// for headers, no response is committed from upstream, a local 504 is written,
// and every reservation is released.
func TestForward_HeaderTimeoutAllCandidates(t *testing.T) {
	ps := NewPools()
	rt := newFakeRT(func(req *http.Request, _ int) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	a := pl("A", 0)
	a.Timeouts = Timeouts{ResponseHeaderMS: 20}
	b := pl("B", 0)
	b.Timeouts = Timeouts{ResponseHeaderMS: 20}
	rec := httptest.NewRecorder()
	res := forwardWithin(t, 3*time.Second, context.Background(), rec, input(rt, ps, []Placement{a, b}, targetsFor("A", "B")))
	if res.ServedEndpoint != "" {
		t.Fatalf("no upstream should have served, got %q", res.ServedEndpoint)
	}
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("all-header-timeout should yield local 504, got %d", rec.Code)
	}
	if u, _ := ps.Used("A"); u != 0 {
		t.Fatal("A capacity leaked")
	}
	if u, _ := ps.Used("B"); u != 0 {
		t.Fatal("B capacity leaked")
	}
}
