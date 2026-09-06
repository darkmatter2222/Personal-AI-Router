// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routeadapter

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nvpair-shared/noderec"
	"nvpair-shared/routing"
)

// --- Phase 4: legacy fallback preserves the request body byte-for-byte --------

func TestRoute_LegacyFallbackPreservesBody(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "x")}
	// No routing metadata anywhere -> handled=false, body must be untouched.
	n := noderec.DirectoryNode{HostUUID: "A"}
	rt := newRouter(f, routing.NewPools(), n)

	const original = `{"model":"chat","messages":[{"role":"user","content":"exact-bytes-é-😀"}],"marker":"KEEP"}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(original))
	rec := httptest.NewRecorder()
	handled, _ := rt.Route(rec, req)
	if handled {
		t.Fatal("legacy cluster must not be handled by the adapter")
	}
	got, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("restored body read error: %v", err)
	}
	if string(got) != original {
		t.Fatalf("legacy fallback must restore the body byte-for-byte:\n got %q\nwant %q", got, original)
	}
	if req.ContentLength != int64(len(original)) {
		t.Fatalf("restored ContentLength = %d, want %d", req.ContentLength, len(original))
	}
}

// --- Phase 5: oversized bodies return 413 without contacting a backend --------

func TestRoute_OversizeBodyReturns413(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "SHOULD-NOT-BE-CALLED")}
	n := dnode("A", "e", er(routing.StrategyDeterministicPriority, 10, 0, model("pa", caps(true, false, false, true), 0, "chat")))
	rt := newRouter(f, routing.NewPools(), n)
	rt.MaxBody = 64

	big := `{"model":"chat","messages":[{"role":"user","content":"` + strings.Repeat("A", 200) + `"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(big))
	req.ContentLength = -1 // force the read path rather than the Content-Length fast path
	rec := httptest.NewRecorder()
	handled, res := rt.Route(rec, req)
	if !handled || rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize body must be a 413: handled=%v code=%d", handled, rec.Code)
	}
	if len(f.calls) != 0 {
		t.Fatalf("no upstream call for an oversized body, got %d", len(f.calls))
	}
	if len(res.Rejections) == 0 || res.Rejections[0].Reason != routing.ReasonRequestTooLarge {
		t.Fatalf("413 must record REQUEST_TOO_LARGE: %+v", res.Rejections)
	}
}

func TestRoute_ContentLengthOverLimitReturns413WithoutReading(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "x")}
	n := dnode("A", "e", er(routing.StrategyDeterministicPriority, 10, 0, model("pa", caps(true, false, false, true), 0, "chat")))
	rt := newRouter(f, routing.NewPools(), n)
	rt.MaxBody = 32

	body := strings.Repeat("A", 1000)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.ContentLength = int64(len(body)) // declared oversize -> reject before reading
	rec := httptest.NewRecorder()
	handled, _ := rt.Route(rec, req)
	if !handled || rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared oversize Content-Length must 413: handled=%v code=%d", handled, rec.Code)
	}
	if len(f.calls) != 0 {
		t.Fatal("no upstream call for a declared-oversize request")
	}
}

func TestRoute_ExactLimitIsAccepted(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "ok")}
	n := dnode("A", "e", er(routing.StrategyDeterministicPriority, 10, 0, model("chat", caps(true, false, false, true), 0, "chat")))
	rt := newRouter(f, routing.NewPools(), n)

	body := textBody()
	rt.MaxBody = int64(len(body)) // exactly at the limit: must be accepted
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	handled, res := rt.Route(rec, req)
	if !handled || res.ServedEndpoint != epid("A", "e") {
		t.Fatalf("a body exactly at MaxBody must be served: handled=%v served=%q code=%d", handled, res.ServedEndpoint, rec.Code)
	}
}

// --- Phase 18: body read errors return 400, never a partial forward ----------

type errReader struct {
	data []byte
	err  error
}

func (e *errReader) Read(p []byte) (int, error) {
	if len(e.data) > 0 {
		n := copy(p, e.data)
		e.data = e.data[n:]
		return n, nil
	}
	return 0, e.err
}
func (e *errReader) Close() error { return nil }

func TestRoute_BodyReadErrorReturns400(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "SHOULD-NOT-BE-CALLED")}
	n := dnode("A", "e", er(routing.StrategyDeterministicPriority, 10, 0, model("pa", caps(true, false, false, true), 0, "chat")))
	rt := newRouter(f, routing.NewPools(), n)

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Body = &errReader{data: []byte(`{"model":"chat"`), err: errors.New("boom")}
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	handled, res := rt.Route(rec, req)
	if !handled || rec.Code != http.StatusBadRequest {
		t.Fatalf("body read error must be a 400: handled=%v code=%d", handled, rec.Code)
	}
	if len(f.calls) != 0 {
		t.Fatal("a read error must never forward a partial body")
	}
	if len(res.Rejections) == 0 || res.Rejections[0].Reason != routing.ReasonBadRequest {
		t.Fatalf("read error must record BAD_REQUEST: %+v", res.Rejections)
	}
}

// --- Phase 17: malformed JSON is a client 400, not a routing 503 -------------

func TestRoute_MalformedJSONReturns400NotRoutingError(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "SHOULD-NOT-BE-CALLED")}
	n := dnode("A", "e", er(routing.StrategyDeterministicPriority, 10, 0, model("pa", caps(true, false, false, true), 0, "chat")))
	rt := newRouter(f, routing.NewPools(), n)
	handled, res, rec := route(rt, `{"model": "chat", this is not json`)
	if !handled {
		t.Fatal("malformed request must be handled")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON must be a 400, got %d (a 503 would be a routing failure)", rec.Code)
	}
	if len(f.calls) != 0 {
		t.Fatal("malformed body must never reach a backend")
	}
	if res.NoEligible {
		t.Fatal("malformed JSON must not be reported as NoEligible (a routing failure)")
	}
}

func TestRoute_EmptyBodyIsNotMalformed(t *testing.T) {
	// An empty body is valid; it simply has no model and routes nowhere (503),
	// which is a routing outcome, not a 400.
	f := &fakeRT{respond: rtOK(200, "x")}
	n := dnode("A", "e", er(routing.StrategyDeterministicPriority, 10, 0, model("pa", caps(true, false, false, true), 0, "chat")))
	rt := newRouter(f, routing.NewPools(), n)
	handled, _, rec := route(rt, ``)
	if !handled || rec.Code == http.StatusBadRequest {
		t.Fatalf("empty body must not be a 400: handled=%v code=%d", handled, rec.Code)
	}
}

// --- Phase 6: mixed legacy/enhanced cluster ----------------------------------

func TestRoute_MixedClusterLegacyNodeStaysReachable(t *testing.T) {
	// A enhanced, B legacy (inventory only), C enhanced. All serve "chat"; default
	// strategy; scheduler prefers the legacy node B.
	a := dnode("A", "e", er(routing.StrategyDefault, 0, 0, model("chat", caps(true, false, false, true), 0)))
	c := dnode("C", "e", er(routing.StrategyDefault, 0, 0, model("chat", caps(true, false, false, true), 0)))
	b := noderec.DirectoryNode{HostUUID: "B", ModelsByEngine: map[string][]string{"e": {"chat"}}}
	f := &fakeRT{respond: rtOK(200, "ok")}
	rt := newRouter(f, routing.NewPools(), a, b, c)
	rt.SchedulerOrder = func() []string { return []string{"B", "A", "C"} }

	_, res, _ := route(rt, textBody())
	if res.ServedEndpoint != epid("B", "e") {
		t.Fatalf("legacy node B must remain a default-strategy candidate; served %q", res.ServedEndpoint)
	}
}

func TestRoute_MixedClusterLegacyNeverGetsCapabilityTraffic(t *testing.T) {
	// A enhanced with vision; B legacy inventory (no declared caps). A vision
	// request must go to A, never to the legacy B on an invented capability.
	a := dnode("A", "e", er(routing.StrategyDefault, 0, 0, model("chat", caps(true, true, false, true), 0)))
	b := noderec.DirectoryNode{HostUUID: "B", ModelsByEngine: map[string][]string{"e": {"chat"}}}
	f := &fakeRT{respond: rtOK(200, "ok")}
	rt := newRouter(f, routing.NewPools(), a, b)
	rt.SchedulerOrder = func() []string { return []string{"B", "A"} } // scheduler prefers legacy B

	_, res, _ := route(rt, imageBody())
	if res.ServedEndpoint != epid("A", "e") {
		t.Fatalf("vision must reach the vision-capable A, not the legacy B; served %q", res.ServedEndpoint)
	}
	if f.count("B") != 0 {
		t.Fatal("a legacy node must never receive capability-specific traffic on an invented capability")
	}
}

// --- Phase 7: enhanced engine with empty Models falls back to inventory ------

func TestRoute_EmptyModelsFallsBackToInventory(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "ok")}
	// Enhanced routing metadata but NO explicit Models; inventory advertises "chat".
	node := noderec.DirectoryNode{
		HostUUID:        "A",
		RoutingByEngine: map[string]routing.EngineRouting{"e": er(routing.StrategyDeterministicPriority, 10, 0)},
		ModelsByEngine:  map[string][]string{"e": {"chat", "other"}},
	}
	rt := newRouter(f, routing.NewPools(), node)
	handled, res, _ := route(rt, textBody())
	if !handled || res.ServedEndpoint != epid("A", "e") {
		t.Fatalf("empty Models must fall back to inventory and serve the advertised model; served %q", res.ServedEndpoint)
	}
	// But an inventory fallback never invents vision.
	f2 := &fakeRT{respond: rtOK(200, "ok")}
	rt2 := newRouter(f2, routing.NewPools(), node)
	_, res2, _ := route(rt2, imageBody())
	if !res2.NoEligible || len(f2.calls) != 0 {
		t.Fatalf("inventory fallback must not invent vision: res=%+v calls=%d", res2, len(f2.calls))
	}
}

// --- Phase 8: strategy is resolved from the ELIGIBLE set only -----------------

func TestRoute_IneligibleDeterministicEndpointDoesNotForceStrategy(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "ok")}
	// A declares deterministic-priority but serves a DIFFERENT model, so it is
	// ineligible for "chat". B and C are default-strategy and eligible. If A's
	// strategy leaked, ordering would be by priority (C=1 wins); with the fix,
	// the eligible set is all-default, so scheduler order (B first) wins.
	a := dnode("A", "eA", er(routing.StrategyDeterministicPriority, 1, 0, model("other-model", caps(true, false, false, true), 0, "other")))
	b := dnode("B", "eB", er(routing.StrategyDefault, 99, 0, model("pb", caps(true, false, false, true), 0, "chat")))
	c := dnode("C", "eC", er(routing.StrategyDefault, 1, 0, model("pc", caps(true, false, false, true), 0, "chat")))
	rt := newRouter(f, routing.NewPools(), a, b, c)
	rt.SchedulerOrder = func() []string { return []string{"B", "C"} }

	_, res, _ := route(rt, textBody())
	if res.ServedEndpoint != epid("B", "eB") {
		t.Fatalf("ineligible A's deterministic strategy must not reorder the eligible default set; served %q (want B)", res.ServedEndpoint)
	}
}

// --- Phase 16: context reserves are configurable ------------------------------

func TestRoute_ConfigurableOutputReserveAffectsContextGate(t *testing.T) {
	// Model has a 4096-token window. A tiny request under the default 512 reserve
	// fits; a large configured output reserve pushes required context past 4096.
	node := dnode("A", "e", er(routing.StrategyDeterministicPriority, 10, 0, model("phys", caps(true, false, false, true), 4096, "chat")))

	f := &fakeRT{respond: rtOK(200, "ok")}
	rt := newRouter(f, routing.NewPools(), node) // default reserves
	handled, res, _ := route(rt, textBody())
	if !handled || res.ServedEndpoint != epid("A", "e") {
		t.Fatalf("small request under default reserve must fit the 4096 window; served %q", res.ServedEndpoint)
	}

	f2 := &fakeRT{respond: rtOK(200, "ok")}
	rt2 := newRouter(f2, routing.NewPools(), node)
	rt2.Reserve = routing.ReservePolicy{OutputReserveTokens: 8192} // exceeds the window
	_, res2, _ := route(rt2, textBody())
	if !res2.NoEligible || len(f2.calls) != 0 {
		t.Fatalf("a large configured output reserve must exceed the window and yield no eligible endpoint: res=%+v", res2)
	}
}
