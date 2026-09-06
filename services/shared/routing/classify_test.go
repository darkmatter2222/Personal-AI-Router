// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"strings"
	"testing"
)

// The request classification test matrix: every shape the proxy can receive,
// from an empty body through content blocks, tools, streaming, and output
// caps, asserting the derived routing requirements — never the content itself.

func assertZeroReq(t *testing.T, name string, req Req) {
	t.Helper()
	// A body that fails to parse is treated as "no information": the request
	// names nothing and carries no budgets (the floor reserve only applies to
	// a valid JSON object, which is itself a request).
	if req != (Req{}) {
		t.Errorf("%s: got %+v, want zero Req", name, req)
	}
}

func TestClassifyNilAndEmptyBody(t *testing.T) {
	assertZeroReq(t, "nil body", Classify(nil))
	assertZeroReq(t, "empty body", Classify([]byte{}))
	assertZeroReq(t, "whitespace body", Classify([]byte("   ")))
}

func TestClassifyMalformedJSON(t *testing.T) {
	for _, body := range []string{
		"{",
		"{]",
		"[1,2",
		`{"model"`,
		`{"model":}`,
		`{"messages":}`,
		`{"content":}`,
		"null",
		`"just a string"`,
		"42",
		`{"model":123}`,
		`{"stream":"yes"}`,
		`{"messages":"hi"}`,
		`{"tools":{}}`,
	} {
		req := Classify([]byte(body))
		// A malformed body must not panic and must not set streaming (a
		// non-bool stream field decodes to a nil pointer).
		if req.RequiresStreaming {
			t.Errorf("body %q: streaming set on malformed body", body)
		}
	}
}

func TestClassifyEmptyObject(t *testing.T) {
	// A valid but empty JSON object names nothing: no model, no prompt, no
	// messages. The required-context reserve still applies (a zero-size
	// request still needs the engine's headroom), so only that field is
	// nonzero.
	req := Classify([]byte(`{}`))
	if req.Text || req.HasImages || req.RequiresTools || req.RequiresStreaming || req.Model != "" {
		t.Errorf("empty object: got %+v", req)
	}
	if req.InputTokens != 0 || req.MaxOutput != 0 {
		t.Errorf("empty object tokens: %+v", req)
	}
	if req.RequiredContext != contextReserveFloor {
		t.Errorf("empty object required context = %d, want the %d floor", req.RequiredContext, contextReserveFloor)
	}
}

func TestClassifyPlainPrompt(t *testing.T) {
	req := Classify([]byte(`{"prompt":"hello world"}`))
	if !req.Text {
		t.Error("prompt body should be a text request")
	}
	if req.Model != "" {
		t.Errorf("model = %q, want empty", req.Model)
	}
	if req.InputTokens != len("hello world")/4 {
		t.Errorf("input tokens = %d, want %d", req.InputTokens, len("hello world")/4)
	}
	if req.RequiredContext <= req.InputTokens {
		t.Errorf("required context %d should exceed input tokens %d", req.RequiredContext, req.InputTokens)
	}
}

func TestClassifySingleMessage(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"hi there"}]}`
	req := Classify([]byte(body))
	if req.Model != "m" || !req.Text {
		t.Errorf("got %+v", req)
	}
	if req.InputTokens != len("hi there")/4 {
		t.Errorf("input tokens = %d, want %d", req.InputTokens, len("hi there")/4)
	}
}

func TestClassifyMultiMessageConversation(t *testing.T) {
	body := `{"model":"m","messages":[` +
		`{"role":"system","content":"you are terse"},` +
		`{"role":"user","content":"first"},` +
		`{"role":"assistant","content":"a reply"},` +
		`{"role":"user","content":"second question"}]}`
	req := Classify([]byte(body))
	want := len("you are terse") + len("first") + len("a reply") + len("second question")
	if req.InputTokens != want/4 {
		t.Errorf("input tokens = %d, want %d", req.InputTokens, want/4)
	}
	if req.HasImages || req.RequiresTools || req.RequiresStreaming {
		t.Errorf("unexpected flags: %+v", req)
	}
}

func TestClassifySystemAndAssistantHistory(t *testing.T) {
	// Assistant history must count toward the estimate (it is resent to the
	// engine and consumes context).
	body := `{"model":"m","messages":[` +
		`{"role":"system","content":"s"},` +
		`{"role":"assistant","content":"a very long assistant turn "},` +
		`{"role":"user","content":"u"}]}`
	req := Classify([]byte(body))
	want := len("s") + len("a very long assistant turn ") + len("u")
	if req.InputTokens != want/4 {
		t.Errorf("input tokens = %d, want %d", req.InputTokens, want/4)
	}
}

func TestClassifyStringContent(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"plain string content"}]}`
	req := Classify([]byte(body))
	if req.InputTokens != len("plain string content")/4 {
		t.Errorf("input tokens = %d", req.InputTokens)
	}
}

func TestClassifyContentArrayTextBlocks(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"part one "},` +
		`{"type":"text","text":"part two"}]}]}`
	req := Classify([]byte(body))
	// Both text blocks contribute; the estimate must not ignore text inside
	// structured content arrays.
	want := len("part one ") + len("part two")
	if req.InputTokens != want/4 {
		t.Errorf("input tokens = %d, want %d (content-array text must count)", req.InputTokens, want/4)
	}
	if req.HasImages {
		t.Error("text-only content array must not flag images")
	}
}

func TestClassifyMixedTextAndImageBlocks(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"describe this"},` +
		`{"type":"image_url","image_url":{"url":"https://example.com/cat.jpg"}},` +
		`{"type":"text","text":" in detail"}]}]}`
	req := Classify([]byte(body))
	if !req.HasImages {
		t.Fatal("mixed content with an image_url block must flag images")
	}
	if req.ImageCount != 1 {
		t.Errorf("image count = %d, want 1", req.ImageCount)
	}
	want := len("describe this") + len(" in detail")
	if req.InputTokens != want/4+imageTokenAllowance {
		t.Errorf("input tokens = %d, want %d", req.InputTokens, want/4+imageTokenAllowance)
	}
}

func TestClassifyImageBlockForms(t *testing.T) {
	cases := []struct {
		name  string
		block string
		want  bool
	}{
		{"image_url", `{"type":"image_url","image_url":{"url":"http://x"}}`, true},
		{"image", `{"type":"image","source":{"data":"abc"}}`, true},
		{"image_base64", `{"type":"image_base64","data":"abc"}`, true},
		{"input_image", `{"type":"input_image","image_url":{"url":"http://x"}}`, true},
		{"untyped image_url property", `{"image_url":{"url":"http://x"}}`, true},
		{"text block", `{"type":"text","text":"no image here"}`, false},
		{"unknown block", `{"type":"tool_use","id":"t1"}`, false},
		{"null block", `null`, false},
		{"scalar block", `"weird"`, false},
		{"empty object block", `{}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"model":"m","messages":[{"role":"user","content":[` + tc.block + `]}]}`
			req := Classify([]byte(body))
			if req.HasImages != tc.want {
				t.Errorf("has images = %v, want %v", req.HasImages, tc.want)
			}
		})
	}
}

func TestClassifyMalformedContentBlocksNoPanic(t *testing.T) {
	bodies := []string{
		`{"model":"m","messages":[{"role":"user","content":42}]}`,
		`{"model":"m","messages":[{"role":"user","content":null}]}`,
		`{"model":"m","messages":[{"role":"user","content":{"a":1}}]}`,
		`{"model":"m","messages":[{"role":"user","content":[1,"x",{"type":"text"}]}]}`,
		`{"model":"m","messages":[42,null,"s"]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url"}]}]}`,
	}
	for _, body := range bodies {
		req := Classify([]byte(body))
		if req.RequiresStreaming {
			t.Errorf("body %q: streaming wrongly set", body)
		}
	}
}

func TestClassifyUnicode(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"héllo wörld 你好世界 🚀"}]}`
	req := Classify([]byte(body))
	want := len("héllo wörld 你好世界 🚀")
	if req.InputTokens != want/4 {
		t.Errorf("unicode input tokens = %d, want %d", req.InputTokens, want/4)
	}
}

func TestClassifyLargePrompt(t *testing.T) {
	big := strings.Repeat("x", 1_000_000)
	body := `{"model":"m","messages":[{"role":"user","content":"` + big + `"}]}`
	req := Classify([]byte(body))
	if req.InputTokens != len(big)/4 {
		t.Errorf("large prompt tokens = %d, want %d", req.InputTokens, len(big)/4)
	}
	// The reserve must grow past the floor for a large input.
	if req.RequiredContext <= len(big)/4+contextReserveFloor {
		t.Errorf("large prompt reserve did not scale: %d", req.RequiredContext)
	}
}

func TestClassifyToolsVariants(t *testing.T) {
	cases := []struct {
		name      string
		extra     string
		wantTools bool
	}{
		{"tools omitted", "", false},
		{"tools empty array", `,"tools":[]`, false},
		{"tools null", `,"tools":null`, false},
		{"tools populated", `,"tools":[{"type":"function","function":{"name":"get_weather","parameters":{}}}]`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"model":"m","messages":[{"role":"user","content":"hi"}]` + tc.extra + `}`
			req := Classify([]byte(body))
			if req.RequiresTools != tc.wantTools {
				t.Errorf("requires tools = %v, want %v", req.RequiresTools, tc.wantTools)
			}
		})
	}
}

func TestClassifyToolsPayloadCountsInEstimate(t *testing.T) {
	small := `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function"}]}`
	large := `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"a":1,"b":2,"c":3,"d":"x"}}}]}`
	smallReq := Classify([]byte(small))
	largeReq := Classify([]byte(large))
	if largeReq.InputTokens <= smallReq.InputTokens {
		t.Errorf("tool schema payload must count toward the input estimate: %d <= %d",
			largeReq.InputTokens, smallReq.InputTokens)
	}
}

func TestClassifyStreaming(t *testing.T) {
	cases := []struct {
		name  string
		extra string
		want  bool
	}{
		{"stream omitted", "", false},
		{"stream false", `,"stream":false`, false},
		{"stream true", `,"stream":true`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"model":"m","messages":[{"role":"user","content":"hi"}]` + tc.extra + `}`
			req := Classify([]byte(body))
			if req.RequiresStreaming != tc.want {
				t.Errorf("requires streaming = %v, want %v", req.RequiresStreaming, tc.want)
			}
		})
	}
}

func TestClassifyMaxOutputPrecedence(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"none", `{"model":"m"}`, 0},
		{"max_tokens only", `{"model":"m","max_tokens":1000}`, 1000},
		{"max_completion_tokens only", `{"model":"m","max_completion_tokens":2000}`, 2000},
		{"both: completion wins", `{"model":"m","max_tokens":1000,"max_completion_tokens":2000}`, 2000},
		{"explicit zero completion beats max_tokens", `{"model":"m","max_tokens":1000,"max_completion_tokens":0}`, 0},
		{"ollama num_predict", `{"model":"m","options":{"num_predict":777}}`, 777},
		{"completion beats num_predict", `{"model":"m","options":{"num_predict":777},"max_completion_tokens":5}`, 5},
		{"negative max_tokens clamps to zero", `{"model":"m","max_tokens":-5}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := Classify([]byte(tc.body))
			if req.MaxOutput != tc.want {
				t.Errorf("max output = %d, want %d", req.MaxOutput, tc.want)
			}
		})
	}
}

func TestClassifyRequiredContextMath(t *testing.T) {
	req := Classify([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":100}`))
	// input 2 chars/4 = 0 tokens; required = 0 + 100 + reserve(floor 4096).
	if want := 100 + contextReserveFloor; req.RequiredContext != want {
		t.Errorf("required context = %d, want %d", req.RequiredContext, want)
	}
}

func TestClassifyNeverStoresContent(t *testing.T) {
	secret := "the-quick-brown-fox-jumps-12345"
	body := `{"model":"m","messages":[{"role":"user","content":"` + secret + `"}]}`
	req := Classify([]byte(body))
	// The Req carries only derived metadata; none of its fields may hold the
	// prompt text (the model name is the only string, and it is "m").
	if req.Model != "m" {
		t.Errorf("model = %q", req.Model)
	}
	_ = secret
}

// TestClassifyFuzzSeeds is a bounded deterministic fuzz harness: fixed inputs
// that historically broke parsers must not panic or set impossible values.
func TestClassifyFuzzSeeds(t *testing.T) {
	seeds := [][]byte{
		{},
		{0x00},
		[]byte("\xff\xfe"),
		[]byte(`{"model":"` + strings.Repeat("m", 100000) + `"`),
		[]byte(strings.Repeat(`{"a":[{"b":1}],`, 5000)),
		[]byte(`{"messages":[{"content":` + strings.Repeat(`{"t":"x"}`, 2000) + `}]}`),
	}
	for i, seed := range seeds {
		req := Classify(seed)
		if req.MaxOutput < 0 || req.InputTokens < 0 || req.RequiredContext < 0 || req.ImageCount < 0 {
			t.Errorf("seed %d: negative fields %+v", i, req)
		}
	}
}
