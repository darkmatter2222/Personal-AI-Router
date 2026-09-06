// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routeadapter

import (
	"net/http"
	"strings"
	"testing"

	"nvpair-shared/noderec"
	"nvpair-shared/routing"
)

// The cross-engine engine ids below deliberately appear in NO production
// enumeration. They prove engine identity does not define the routing universe:
// one logical alias (local-coding) spans three different runtimes on three nodes.
const (
	engVLLM     = "engine-vllm-test"
	engLlamaCpp = "engine-llamacpp-test"
	engVLM      = "engine-vlm-test"
)

// crossNodes builds the canonical heterogeneous topology: one alias, three
// engines, three nodes, ascending priority, C alone has vision.
func crossNodes() (a, b, c noderec.DirectoryNode) {
	a = dnode("A", engVLLM, er(routing.StrategyDeterministicPriority, 10, 1, model("qwen-fast", caps(true, false, true, true), 0, "local-coding")))
	b = dnode("B", engLlamaCpp, er(routing.StrategyDeterministicPriority, 20, 1, model("qwen-q4", caps(true, false, true, true), 0, "local-coding")))
	c = dnode("C", engVLM, er(routing.StrategyDeterministicPriority, 30, 3, model("flash-next", caps(true, true, true, true), 0, "local-coding")))
	return a, b, c
}

func codingBody() string {
	return `{"model":"local-coding","messages":[{"role":"user","content":"hi"}]}`
}
func codingVisionBody() string {
	return `{"model":"local-coding","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`
}

// TestCrossEngine_HeterogeneousRouting is the definitive proof that one logical
// request considers candidates across DIFFERENT engine ids and that the outbound
// request always carries the chosen endpoint's own physical model name.
func TestCrossEngine_HeterogeneousRouting(t *testing.T) {
	a, b, c := crossNodes()

	t.Run("normal text prefers A and rewrites to its physical", func(t *testing.T) {
		f := &fakeRT{respond: rtOK(200, "ok")}
		rt := newRouter(f, routing.NewPools(), a, b, c)
		_, res, _ := route(rt, codingBody())
		if res.ServedEndpoint != epid("A", engVLLM) {
			t.Fatalf("normal text must route to A (priority 10); served %q", res.ServedEndpoint)
		}
		got, _ := f.last("A")
		if !strings.Contains(got.body, `"model":"qwen-fast"`) {
			t.Fatalf("A must receive its own physical model qwen-fast: %q", got.body)
		}
	})

	t.Run("A full spills to B with B's physical", func(t *testing.T) {
		f := &fakeRT{respond: rtOK(200, "ok")}
		ps := routing.NewPools()
		hold, _ := ps.Reserve(epid("A", engVLLM), 1)
		defer hold()
		rt := newRouter(f, ps, a, b, c)
		_, res, _ := route(rt, codingBody())
		if res.ServedEndpoint != epid("B", engLlamaCpp) {
			t.Fatalf("A full must spill to B; served %q", res.ServedEndpoint)
		}
		got, _ := f.last("B")
		if !strings.Contains(got.body, `"model":"qwen-q4"`) {
			t.Fatalf("B must receive its own physical qwen-q4: %q", got.body)
		}
		if f.count("A") != 0 {
			t.Fatalf("A must not be dialled when full")
		}
	})

	t.Run("A and B full spills to C", func(t *testing.T) {
		f := &fakeRT{respond: rtOK(200, "ok")}
		ps := routing.NewPools()
		ha, _ := ps.Reserve(epid("A", engVLLM), 1)
		hb, _ := ps.Reserve(epid("B", engLlamaCpp), 1)
		defer ha()
		defer hb()
		rt := newRouter(f, ps, a, b, c)
		_, res, _ := route(rt, codingBody())
		if res.ServedEndpoint != epid("C", engVLM) {
			t.Fatalf("A+B full must spill to C; served %q", res.ServedEndpoint)
		}
		got, _ := f.last("C")
		if !strings.Contains(got.body, `"model":"flash-next"`) {
			t.Fatalf("C must receive its own physical flash-next: %q", got.body)
		}
	})

	t.Run("vision routes only to C", func(t *testing.T) {
		f := &fakeRT{respond: rtOK(200, "ok")}
		rt := newRouter(f, routing.NewPools(), a, b, c)
		_, res, _ := route(rt, codingVisionBody())
		if res.ServedEndpoint != epid("C", engVLM) {
			t.Fatalf("vision must route to the only vision engine C; served %q", res.ServedEndpoint)
		}
		if f.count("A") != 0 || f.count("B") != 0 {
			t.Fatalf("no non-vision engine may be dialled for a vision request")
		}
	})

	t.Run("A unhealthy spills to B", func(t *testing.T) {
		f := &fakeRT{respond: rtOK(200, "ok")}
		rt := newRouter(f, routing.NewPools(), a, b, c)
		rt.Healthy = func(n noderec.DirectoryNode) bool { return n.HostUUID != "A" }
		_, res, _ := route(rt, codingBody())
		if res.ServedEndpoint != epid("B", engLlamaCpp) {
			t.Fatalf("A unhealthy must spill to B; served %q", res.ServedEndpoint)
		}
		if f.count("A") != 0 {
			t.Fatalf("unhealthy A must not be dialled")
		}
	})

	t.Run("A retryable pre-stream failure fails over to B", func(t *testing.T) {
		f := &fakeRT{respond: func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == "A" {
				return rtOK(503, "a-down")(r) // retryable status before any stream commit
			}
			return rtOK(200, "good")(r)
		}}
		rt := newRouter(f, routing.NewPools(), a, b, c)
		_, res, rec := route(rt, codingBody())
		if res.ServedEndpoint != epid("B", engLlamaCpp) || rec.Body.String() != "good" {
			t.Fatalf("A retryable failure must fail over to B; served %q body %q", res.ServedEndpoint, rec.Body.String())
		}
		if f.count("A") != 1 {
			t.Fatalf("A should have been tried exactly once before failover, got %d", f.count("A"))
		}
	})
}

// dnode2 builds one node hosting TWO engines.
func dnode2(uuid string, e1 string, r1 routing.EngineRouting, e2 string, r2 routing.EngineRouting) noderec.DirectoryNode {
	return noderec.DirectoryNode{HostUUID: uuid, RoutingByEngine: map[string]routing.EngineRouting{e1: r1, e2: r2}}
}

// TestSameNode_MultiEngine proves two engines on ONE node are distinct endpoints
// (independent admission, independent rewrite, independent priority) that share
// one node identity (Phase 29 + NodeID/EndpointID separation).
func TestSameNode_MultiEngine(t *testing.T) {
	const e1, e2 = "engine-one", "engine-two"
	// e2 has the lower (preferred) priority number, so it wins when both are free.
	node := dnode2("A",
		e1, er(routing.StrategyDeterministicPriority, 20, 1, model("phys-one", caps(true, false, false, true), 0, "local-coding")),
		e2, er(routing.StrategyDeterministicPriority, 10, 1, model("phys-two", caps(true, false, false, true), 0, "local-coding")),
	)

	t.Run("distinct endpoint ids, preferred engine served", func(t *testing.T) {
		f := &fakeRT{respond: rtOK(200, "ok")}
		rt := newRouter(f, routing.NewPools(), node)
		_, res, _ := route(rt, codingBody())
		if res.ServedEndpoint != epid("A", e2) {
			t.Fatalf("lower-priority engine-two must win; served %q", res.ServedEndpoint)
		}
		if epid("A", e1) == epid("A", e2) {
			t.Fatal("the two same-node endpoints must have distinct EndpointIDs")
		}
		got, _ := f.last("A")
		if !strings.Contains(got.body, `"model":"phys-two"`) {
			t.Fatalf("served endpoint must rewrite to its own physical phys-two: %q", got.body)
		}
	})

	t.Run("filling one engine's pool does not fill the other's", func(t *testing.T) {
		f := &fakeRT{respond: rtOK(200, "ok")}
		ps := routing.NewPools()
		// Occupy the preferred engine-two's only slot; engine-one must still serve.
		hold, _ := ps.Reserve(epid("A", e2), 1)
		defer hold()
		rt := newRouter(f, ps, node)
		_, res, _ := route(rt, codingBody())
		if res.ServedEndpoint != epid("A", e1) {
			t.Fatalf("engine-two full must spill to engine-one on the SAME node; served %q", res.ServedEndpoint)
		}
		if u, _ := ps.Used(epid("A", e2)); u != 1 {
			t.Fatalf("engine-two pool should still hold its single reservation, used=%d", u)
		}
	})

	t.Run("shared node rank under default strategy", func(t *testing.T) {
		// Under the default strategy both endpoints share node A's scheduler rank;
		// the EndpointID tie-break then orders them deterministically.
		f := &fakeRT{respond: rtOK(200, "ok")}
		def := dnode2("A",
			e1, er(routing.StrategyDefault, 0, 1, model("phys-one", caps(true, false, false, true), 0, "local-coding")),
			e2, er(routing.StrategyDefault, 0, 1, model("phys-two", caps(true, false, false, true), 0, "local-coding")),
		)
		rt := newRouter(f, routing.NewPools(), def)
		rt.SchedulerOrder = func() []string { return []string{"A"} }
		_, res, _ := route(rt, codingBody())
		// Both share rank(A); tie-break by EndpointID -> the smaller key wins.
		want := epid("A", e1)
		if epid("A", e2) < want {
			want = epid("A", e2)
		}
		if res.ServedEndpoint != want {
			t.Fatalf("default strategy tie-break by EndpointID; served %q want %q", res.ServedEndpoint, want)
		}
	})
}

// TestCrossEngine_MixedAPIFamily proves an OpenAI request never routes to an
// Ollama-native endpoint merely because a model alias matches (Phase 28). The
// family gate applies across engines just like every other gate.
func TestCrossEngine_MixedAPIFamily(t *testing.T) {
	f := &fakeRT{respond: rtOK(200, "ok")}
	// Node A: OpenAI-family engine. Node B: Ollama-native engine. Both alias "chat".
	oai := er(routing.StrategyDeterministicPriority, 20, 0, model("openai-phys", caps(true, false, false, true), 0, "chat"))
	ollama := routing.EngineRouting{
		APIFamily: routing.APIFamilyOllama, Strategy: routing.StrategyDeterministicPriority,
		Priority: intp(10), Models: []routing.ModelRouting{model("ollama-phys", caps(true, false, false, true), 0, "chat")},
	}
	a := dnode("A", "engine-oai", oai)
	b := dnode("B", "engine-ollama", ollama)
	rt := newRouter(f, routing.NewPools(), a, b) // surface APIFamily = OpenAI
	_, res, _ := route(rt, textBody())
	if res.ServedEndpoint != epid("A", "engine-oai") {
		t.Fatalf("OpenAI request must not route to the Ollama-native (lower-priority) B; served %q", res.ServedEndpoint)
	}
	if f.count("B") != 0 {
		t.Fatalf("the Ollama-native endpoint must not be dialled for an OpenAI request")
	}
}

func intp(i int) *int { return &i }
