// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routeadapter

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nvpair-shared/noderec"
	"nvpair-shared/routing"
)

// --- fakes ------------------------------------------------------------------

type capture struct {
	host, path, body string
	header           http.Header
}

type fakeRT struct {
	mu      sync.Mutex
	calls   []capture
	respond func(*http.Request) (*http.Response, error)
}

func (f *fakeRT) RoundTrip(req *http.Request) (*http.Response, error) {
	var b []byte
	if req.Body != nil {
		b, _ = io.ReadAll(req.Body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, capture{req.URL.Host, req.URL.Path, string(b), req.Header.Clone()})
	f.mu.Unlock()
	return f.respond(req)
}
func (f *fakeRT) count(host string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.host == host {
			n++
		}
	}
	return n
}
func (f *fakeRT) last(host string) (capture, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].host == host {
			return f.calls[i], true
		}
	}
	return capture{}, false
}
func rtOK(code int, body string) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
}

// --- builders ---------------------------------------------------------------

// epid mirrors the adapter's EndpointID construction so tests can key targets,
// admission pools and served-endpoint assertions by the same collision-free key.
func epid(host, engine string) string { return routing.EndpointKey(host, engine) }

func caps(text, vision, tools, streaming bool) routing.Capabilities {
	return routing.Capabilities{Text: text, Vision: vision, Tools: tools, Streaming: streaming}
}
func model(physical string, c routing.Capabilities, maxTok int, aliases ...string) routing.ModelRouting {
	return routing.ModelRouting{Physical: physical, Aliases: aliases, Capabilities: c, Context: routing.Context{MaxTokens: maxTok}}
}
func er(strategy routing.Strategy, priority, capacity int, models ...routing.ModelRouting) routing.EngineRouting {
	p := priority
	return routing.EngineRouting{APIFamily: routing.APIFamilyOpenAI, Strategy: strategy, Priority: &p, Capacity: capacity, Models: models}
}
func dnode(uuid, engine string, r routing.EngineRouting) noderec.DirectoryNode {
	return noderec.DirectoryNode{HostUUID: uuid, RoutingByEngine: map[string]routing.EngineRouting{engine: r}}
}

// newRouter wires a cross-engine Router (EngineFilter nil = all engines). Targets
// are keyed by EndpointID; each endpoint's BaseURL host is the node's UUID so a
// fakeRT can count dials by host for single-engine-per-node topologies.
func newRouter(rt http.RoundTripper, ps *routing.Pools, nodes ...noderec.DirectoryNode) *Router {
	targets := map[string]routing.Target{}
	add := func(host, engine string) {
		key := epid(host, engine)
		if _, ok := targets[key]; !ok {
			targets[key] = routing.Target{BaseURL: "http://" + host}
		}
	}
	for _, n := range nodes {
		for engine := range n.RoutingByEngine {
			add(n.HostUUID, engine)
		}
		for engine := range n.ModelsByEngine {
			add(n.HostUUID, engine)
		}
	}
	return &Router{
		APIFamily:    routing.APIFamilyOpenAI,
		Snapshot:     func() []noderec.DirectoryNode { return nodes },
		Healthy:      func(n noderec.DirectoryNode) bool { return true },
		Pools:        ps,
		Transport:    rt,
		TargetFor:    func(id string) (routing.Target, bool) { t, ok := targets[id]; return t, ok },
		Defaults:     routing.TimeoutDefaults{ResponseHeader: 5 * time.Second, FirstByte: 5 * time.Second},
		IsInference:  true,
		RewriteModel: true,
	}
}

func route(rt *Router, body string) (bool, routing.ForwardResult, *httptest.ResponseRecorder) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handled, res := rt.Route(rec, req)
	return handled, res, rec
}

// text/vision/tools helper bodies for logical model "chat".
func textBody() string  { return `{"model":"chat","messages":[{"role":"user","content":"hi"}]}` }
func imageBody() string { return `{"model":"chat","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}` }
func toolsBody() string {
	return `{"model":"chat","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f"}}]}`
}

// --- tests ------------------------------------------------------------------

func TestRoute_LegacyFallbackWhenNoMetadata(t *testing.T) {
	// Nodes carry NO routing metadata for the engine -> Route declines (legacy).
	f := &fakeRT{respond: rtOK(200, "x")}
	n := noderec.DirectoryNode{HostUUID: "A"} // no RoutingByEngine
	rt := newRouter(f, routing.NewPools(), n)
	handled, _, rec := route(rt, textBody())
	if handled {
		t.Fatal("no routing metadata must fall back to legacy (handled=false)")
	}
	if rec.Code != 200 || rec.Body.Len() != 0 {
		// Route must not have written anything.
		t.Fatalf("Route must not write on legacy fallback: code=%d body=%q", rec.Code, rec.Body.String())
	}
	if len(f.calls) != 0 {
		t.Fatal("no upstream call on legacy fallback")
	}
}

func TestRoute_BasicText(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "hello")}
	n := dnode("A", "e", er(routing.StrategyDeterministicPriority, 10, 0, model("phys-A", caps(true, false, true, true), 0, "chat")))
	rt := newRouter(f, routing.NewPools(), n)
	handled, res, rec := route(rt, textBody())
	if !handled || res.ServedEndpoint != epid("A", "e") || rec.Body.String() != "hello" {
		t.Fatalf("handled=%v served=%q body=%q", handled, res.ServedEndpoint, rec.Body.String())
	}
}

func TestRoute_VisionGate(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "ok")}
	a := dnode("A", "e", er(routing.StrategyDeterministicPriority, 10, 0, model("pa", caps(true, false, false, true), 0, "chat")))
	b := dnode("B", "e", er(routing.StrategyDeterministicPriority, 20, 0, model("pb", caps(true, false, false, true), 0, "chat")))
	c := dnode("C", "e", er(routing.StrategyDeterministicPriority, 30, 0, model("pc", caps(true, true, false, true), 0, "chat")))
	rt := newRouter(f, routing.NewPools(), a, b, c)
	handled, res, _ := route(rt, imageBody())
	if !handled || res.ServedEndpoint != epid("C", "e") {
		t.Fatalf("image request must route only to the vision node C; served %q", res.ServedEndpoint)
	}
	if f.count("A") != 0 || f.count("B") != 0 || f.count("C") != 1 {
		t.Fatalf("only C should be dialled: A=%d B=%d C=%d", f.count("A"), f.count("B"), f.count("C"))
	}
}

func TestRoute_ToolsGatePrefersCapableOverHigherPriority(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "ok")}
	// A is higher priority (10) but cannot do tools; B (20) can.
	a := dnode("A", "e", er(routing.StrategyDeterministicPriority, 10, 0, model("pa", caps(true, false, false, true), 0, "chat")))
	b := dnode("B", "e", er(routing.StrategyDeterministicPriority, 20, 0, model("pb", caps(true, false, true, true), 0, "chat")))
	rt := newRouter(f, routing.NewPools(), a, b)
	_, res, _ := route(rt, toolsBody())
	if res.ServedEndpoint != epid("B", "e") {
		t.Fatalf("tools request must reach the capable lower-priority B, served %q", res.ServedEndpoint)
	}
	if f.count("A") != 0 {
		t.Fatal("the tools-incapable higher-priority A must not be dialled")
	}
}

func TestRoute_AliasRewrittenToPhysical(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "ok")}
	n := dnode("A", "e", er(routing.StrategyDeterministicPriority, 10, 0, model("qwen-fast", caps(true, false, true, true), 0, "local-coding")))
	rt := newRouter(f, routing.NewPools(), n)
	route(rt, `{"model":"local-coding","messages":[{"role":"user","content":"hi"}]}`)
	got, ok := f.last("A")
	if !ok || !strings.Contains(got.body, `"model":"qwen-fast"`) {
		t.Fatalf("outbound body must rewrite alias to physical: %q", got.body)
	}
	if strings.Contains(got.body, "local-coding") {
		t.Fatalf("alias must not leak upstream: %q", got.body)
	}
}

func TestRoute_CapacitySpillover(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "ok")}
	ps := routing.NewPools()
	hold, _ := ps.Reserve(epid("A", "e"), 1) // occupy A's single slot
	defer hold()
	a := dnode("A", "e", er(routing.StrategyDeterministicPriority, 10, 1, model("pa", caps(true, false, false, true), 0, "chat")))
	b := dnode("B", "e", er(routing.StrategyDeterministicPriority, 20, 2, model("pb", caps(true, false, false, true), 0, "chat")))
	rt := newRouter(f, ps, a, b)
	_, res, _ := route(rt, textBody())
	if res.ServedEndpoint != epid("B", "e") || f.count("A") != 0 {
		t.Fatalf("full A should spill to B: served %q, A dials %d", res.ServedEndpoint, f.count("A"))
	}
}

func TestRoute_DeterministicPriority(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "ok")}
	a := dnode("A", "e", er(routing.StrategyDeterministicPriority, 10, 0, model("pa", caps(true, false, false, true), 0, "chat")))
	b := dnode("B", "e", er(routing.StrategyDeterministicPriority, 20, 0, model("pb", caps(true, false, false, true), 0, "chat")))
	// Scheduler would prefer B, but deterministic priority must pick A.
	rt := newRouter(f, routing.NewPools(), a, b)
	rt.SchedulerOrder = func() []string { return []string{"B", "A"} }
	_, res, _ := route(rt, textBody())
	if res.ServedEndpoint != epid("A", "e") {
		t.Fatalf("deterministic priority must pick A (10) over B (20); served %q", res.ServedEndpoint)
	}
}

func TestRoute_DefaultSchedulerOrder(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "ok")}
	// Default strategy: scheduler order wins, priority is ignored.
	a := dnode("A", "e", er(routing.StrategyDefault, 10, 0, model("pa", caps(true, false, false, true), 0, "chat")))
	b := dnode("B", "e", er(routing.StrategyDefault, 99, 0, model("pb", caps(true, false, false, true), 0, "chat")))
	rt := newRouter(f, routing.NewPools(), a, b)
	rt.SchedulerOrder = func() []string { return []string{"B", "A"} }
	_, res, _ := route(rt, textBody())
	if res.ServedEndpoint != epid("B", "e") {
		t.Fatalf("default strategy must follow scheduler order (B first); served %q", res.ServedEndpoint)
	}
}

func TestRoute_NoEligibleWritesLocalErrorNoUpstream(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "SHOULD-NOT-BE-CALLED")}
	// All nodes text-only; an image request has no eligible endpoint.
	a := dnode("A", "e", er(routing.StrategyDeterministicPriority, 10, 0, model("pa", caps(true, false, false, true), 0, "chat")))
	rt := newRouter(f, routing.NewPools(), a)
	handled, res, rec := route(rt, imageBody())
	if !handled || !res.NoEligible {
		t.Fatalf("no eligible endpoint should be handled locally: handled=%v res=%+v", handled, res)
	}
	if len(f.calls) != 0 {
		t.Fatal("no upstream call when nothing is eligible")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no-eligible should be a local 503, got %d", rec.Code)
	}
}

func TestRoute_UnhealthyExcluded(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "ok")}
	a := dnode("A", "e", er(routing.StrategyDeterministicPriority, 10, 0, model("pa", caps(true, false, false, true), 0, "chat")))
	rt := newRouter(f, routing.NewPools(), a)
	rt.Healthy = func(noderec.DirectoryNode) bool { return false } // A is unhealthy
	handled, res, _ := route(rt, textBody())
	if !handled || !res.NoEligible || len(f.calls) != 0 {
		t.Fatalf("unhealthy node must be excluded and not dialled: res=%+v calls=%d", res, len(f.calls))
	}
}

func TestRoute_GenericRandomEngine(t *testing.T) {
	// An engine id that appears in NO production enumeration participates via the
	// generic OpenAI-compatible path, with alias rewrite, purely from metadata.
	const genericEngine = "engine-test-847291"
	f := &fakeRT{respond: rtOK(200, "ok")}
	n := dnode("N", genericEngine, er(routing.StrategyDeterministicPriority, 5, 0, model("strange-model-123", caps(true, false, true, true), 0, "local-coding")))
	rt := newRouter(f, routing.NewPools(), n)
	handled, res, _ := route(rt, `{"model":"local-coding","messages":[{"role":"user","content":"hi"}]}`)
	if !handled || res.ServedEndpoint != epid("N", genericEngine) {
		t.Fatalf("generic engine must route via metadata alone: handled=%v served=%q", handled, res.ServedEndpoint)
	}
	got, _ := f.last("N")
	if !strings.Contains(got.body, `"model":"strange-model-123"`) {
		t.Fatalf("generic engine alias should rewrite to its physical: %q", got.body)
	}
}

func TestRoute_MultiModelPerEndpoint(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "ok")}
	// One endpoint, distinct models: only "agent" supports tools.
	n := dnode("A", "e", er(routing.StrategyDeterministicPriority, 10, 0,
		model("text-32k", caps(true, false, false, true), 32768, "fast"),
		model("tools-262k", caps(true, false, true, true), 262144, "agent"),
	))
	rt := newRouter(f, routing.NewPools(), n)

	// tools request for the tools model -> served.
	_, res, _ := route(rt, `{"model":"agent","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f"}}]}`)
	if res.ServedEndpoint != epid("A", "e") {
		t.Fatalf("tools request to the tools model should be served; %+v", res)
	}
	// tools request for the text model -> no eligible.
	f2 := &fakeRT{respond: rtOK(200, "ok")}
	rt2 := newRouter(f2, routing.NewPools(), n)
	handled, res2, _ := route(rt2, `{"model":"fast","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f"}}]}`)
	if !handled || !res2.NoEligible || len(f2.calls) != 0 {
		t.Fatalf("tools request to the non-tools model must be ineligible: res=%+v calls=%d", res2, len(f2.calls))
	}
}
