// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClassify_NilAndMalformed(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		[]byte("   "),
		[]byte("{bad json"),
		[]byte("[1,2,3]"),      // array, not object
		[]byte("\"just a string\""),
		[]byte("null"),
		[]byte("42"),
		[]byte(`{"model": 123}`), // wrong type for model
		[]byte(`{"messages": "not-an-array"}`),
		[]byte(`{"messages": [null, 5, "x", {"role":1}]}`),
		[]byte(`{"tools": "nope", "stream": "yes"}`),
	}
	for i, body := range cases {
		// The contract: never panic, always return a value.
		req := Classify(body)
		_ = req
		if i == 0 || i == 1 {
			if (req != Requirements{}) {
				t.Fatalf("empty body must yield zero Requirements, got %+v", req)
			}
		}
	}
}

func TestClassify_ModelAndFlags(t *testing.T) {
	cases := []struct {
		name           string
		body           string
		wantModel      string
		wantImages     bool
		wantTools      bool
		wantStream     bool
		wantOutput     int
		wantInputGT0   bool
	}{
		{"empty object", `{}`, "", false, false, false, 0, false},
		{"plain prompt", `{"model":"m","prompt":"hello world"}`, "m", false, false, false, 0, true},
		{"prompt array", `{"model":"m","prompt":["hello","world"]}`, "m", false, false, false, 0, true},
		{"single message", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, "m", false, false, false, 0, true},
		{"system+user+assistant", `{"model":"m","messages":[{"role":"system","content":"be nice"},{"role":"user","content":"q"},{"role":"assistant","content":"a"}]}`, "m", false, false, false, 0, true},
		{"array text blocks", `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"block text here"}]}]}`, "m", false, false, false, 0, true},
		{"mixed text+image_url", `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"http://x/y.png"}}]}]}`, "m", true, false, false, 0, true},
		{"image block", `{"model":"m","messages":[{"role":"user","content":[{"type":"image","image":"...."}]}]}`, "m", true, false, false, 0, true},
		{"image_base64 block", `{"model":"m","messages":[{"role":"user","content":[{"type":"image_base64","image_base64":"AAAA"}]}]}`, "m", true, false, false, 0, true},
		{"input_image block", `{"model":"m","messages":[{"role":"user","content":[{"type":"input_image","input_image":{"image_url":"x"}}]}]}`, "m", true, false, false, 0, true},
		{"anthropic image source", `{"model":"m","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"AA"}}]}]}`, "m", true, false, false, 0, true},
		{"image via field no type", `{"model":"m","messages":[{"role":"user","content":[{"image_url":{"url":"x"}}]}]}`, "m", true, false, false, 0, true},
		{"ollama top-level images", `{"model":"m","prompt":"hi","images":["b64data"]}`, "m", true, false, false, 0, true},
		{"ollama message images", `{"model":"m","messages":[{"role":"user","content":"hi","images":["b64"]}]}`, "m", true, false, false, 0, true},
		{"unknown block no panic", `{"model":"m","messages":[{"role":"user","content":[{"type":"weird","foo":1}]}]}`, "m", false, false, false, 0, true},
		{"tools populated", `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f"}}]}`, "m", false, true, false, 0, true},
		{"tools empty", `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[]}`, "m", false, false, false, 0, true},
		{"legacy functions", `{"model":"m","prompt":"hi","functions":[{"name":"f"}]}`, "m", false, true, false, 0, true},
		{"tool_calls history", `{"model":"m","messages":[{"role":"assistant","content":"","tool_calls":[{"id":"1"}]}]}`, "m", false, true, false, 0, true},
		{"stream true", `{"model":"m","prompt":"hi","stream":true}`, "m", false, false, true, 0, true},
		{"stream false", `{"model":"m","prompt":"hi","stream":false}`, "m", false, false, false, 0, true},
		{"max_tokens", `{"model":"m","prompt":"hi","max_tokens":100}`, "m", false, false, false, 100, true},
		{"max_completion_tokens", `{"model":"m","prompt":"hi","max_completion_tokens":200}`, "m", false, false, false, 200, true},
		{"precedence completion over max", `{"model":"m","prompt":"hi","max_tokens":100,"max_completion_tokens":200}`, "m", false, false, false, 200, true},
		{"ollama num_predict top", `{"model":"m","prompt":"hi","num_predict":50}`, "m", false, false, false, 50, true},
		{"ollama options num_predict", `{"model":"m","prompt":"hi","options":{"num_predict":75}}`, "m", false, false, false, 75, true},
		{"max_tokens beats num_predict", `{"model":"m","prompt":"hi","max_tokens":10,"num_predict":99}`, "m", false, false, false, 10, true},
		{"zero output", `{"model":"m","prompt":"hi","max_tokens":0}`, "m", false, false, false, 0, true},
		{"negative num_predict clamped", `{"model":"m","prompt":"hi","num_predict":-1}`, "m", false, false, false, 0, true},
		{"unicode", `{"model":"m","messages":[{"role":"user","content":"héllo 世界 🌍"}]}`, "m", false, false, false, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := Classify([]byte(c.body))
			if req.Model != c.wantModel {
				t.Errorf("Model = %q, want %q", req.Model, c.wantModel)
			}
			if req.HasImages != c.wantImages {
				t.Errorf("HasImages = %v, want %v", req.HasImages, c.wantImages)
			}
			if req.RequiresTools != c.wantTools {
				t.Errorf("RequiresTools = %v, want %v", req.RequiresTools, c.wantTools)
			}
			if req.RequiresStream != c.wantStream {
				t.Errorf("RequiresStream = %v, want %v", req.RequiresStream, c.wantStream)
			}
			if req.OutputTokensReq != c.wantOutput {
				t.Errorf("OutputTokensReq = %d, want %d", req.OutputTokensReq, c.wantOutput)
			}
			if (req.InputTokensEst > 0) != c.wantInputGT0 {
				t.Errorf("InputTokensEst = %d, want >0 == %v", req.InputTokensEst, c.wantInputGT0)
			}
			if req.RequiredContext != req.InputTokensEst+req.OutputTokensReq {
				t.Errorf("RequiredContext = %d, want input(%d)+output(%d)", req.RequiredContext, req.InputTokensEst, req.OutputTokensReq)
			}
		})
	}
}

func TestClassify_LargePromptScales(t *testing.T) {
	small := Classify([]byte(`{"model":"m","prompt":"` + strings.Repeat("a", 100) + `"}`))
	large := Classify([]byte(`{"model":"m","prompt":"` + strings.Repeat("a", 100000) + `"}`))
	if large.InputTokensEst <= small.InputTokensEst {
		t.Fatalf("large prompt (%d) should estimate more tokens than small (%d)", large.InputTokensEst, small.InputTokensEst)
	}
	// Conservative: 100k chars should be at least ~25k tokens (over 4 chars/token).
	if large.InputTokensEst < 25000 {
		t.Fatalf("100k chars estimated only %d tokens; estimator not conservative enough", large.InputTokensEst)
	}
}

func TestClassify_LargeUnicode(t *testing.T) {
	// Multi-byte runes must count as characters, not bytes-per-rune inflated.
	body := `{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("世", 1000) + `"}]}`
	req := Classify([]byte(body))
	if req.InputTokensEst <= 0 {
		t.Fatal("unicode content produced zero token estimate")
	}
}

func TestClassify_ContextGrowsWithTools(t *testing.T) {
	noTools := Classify([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	withTools := Classify([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","description":"` + strings.Repeat("x", 500) + `"}}]}`))
	if withTools.InputTokensEst <= noTools.InputTokensEst {
		t.Fatalf("tool schemas should add to the input estimate: %d vs %d", withTools.InputTokensEst, noTools.InputTokensEst)
	}
}

func TestDecodeContentHelpers(t *testing.T) {
	// decodeContent: string form.
	txt, img := decodeContent(json.RawMessage(`"plain string"`))
	if txt != "plain string" || img {
		t.Fatalf("string content = (%q,%v)", txt, img)
	}
	// decodeContent: block array with text + image.
	txt, img = decodeContent(json.RawMessage(`[{"type":"text","text":"a"},{"type":"text","text":"b"},{"type":"image_url","image_url":{"url":"x"}}]`))
	if txt != "ab" || !img {
		t.Fatalf("blocks = (%q,%v), want (ab,true)", txt, img)
	}
	// decodeContent: malformed -> empty, no panic.
	txt, img = decodeContent(json.RawMessage(`{bad`))
	if txt != "" || img {
		t.Fatalf("malformed content = (%q,%v)", txt, img)
	}
	// decodeBlock: bare string element.
	txt, isImg := decodeBlock(json.RawMessage(`"bare"`))
	if txt != "bare" || isImg {
		t.Fatalf("bare block = (%q,%v)", txt, isImg)
	}
	// rawTextChars: string, array, other.
	if got := rawTextChars(json.RawMessage(`"abc"`)); got != 3 {
		t.Fatalf("rawTextChars string = %d", got)
	}
	if got := rawTextChars(json.RawMessage(`["ab","c"]`)); got != 3 {
		t.Fatalf("rawTextChars array = %d", got)
	}
	if got := rawTextChars(json.RawMessage(`{"x":1}`)); got != 0 {
		t.Fatalf("rawTextChars object = %d", got)
	}
	if got := rawTextChars(nil); got != 0 {
		t.Fatalf("rawTextChars nil = %d", got)
	}
}

func TestEstimateTokensFromChars(t *testing.T) {
	if estimateTokensFromChars(0) != 0 {
		t.Fatal("0 chars -> 0 tokens")
	}
	if estimateTokensFromChars(-5) != 0 {
		t.Fatal("negative chars -> 0 tokens")
	}
	if estimateTokensFromChars(1) < 1 {
		t.Fatal("1 char -> at least 1 token")
	}
	// Monotonic non-decreasing.
	prev := 0
	for _, n := range []int{1, 3, 4, 10, 33, 34, 100, 1000} {
		got := estimateTokensFromChars(n)
		if got < prev {
			t.Fatalf("not monotonic at %d: %d < %d", n, got, prev)
		}
		prev = got
	}
}

func TestResolveOutputTokensPrecedence(t *testing.T) {
	cases := []struct {
		r    rawRequest
		want int
	}{
		{rawRequest{MaxCompletionTokens: intp(1), MaxTokens: intp(2), NumPredict: intp(3)}, 1},
		{rawRequest{MaxTokens: intp(2), NumPredict: intp(3)}, 2},
		{rawRequest{NumPredict: intp(3), Options: &rawOptions{NumPredict: intp(4)}}, 3},
		{rawRequest{Options: &rawOptions{NumPredict: intp(4)}}, 4},
		{rawRequest{}, 0},
		{rawRequest{MaxTokens: intp(-9)}, 0},
	}
	for i, c := range cases {
		if got := resolveOutputTokens(c.r); got != c.want {
			t.Errorf("case %d: resolveOutputTokens = %d, want %d", i, got, c.want)
		}
	}
	if clampNonNeg(-1) != 0 || clampNonNeg(5) != 5 {
		t.Fatal("clampNonNeg wrong")
	}
}
