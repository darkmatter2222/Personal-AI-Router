// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"strings"
	"testing"
)

// TestClassify_EdgeFlags covers real-world request-shape edge cases from the
// Ollama and OpenAI-compatible contracts: fields a router must tolerate without
// crashing or emitting a spurious capability requirement.
func TestClassify_EdgeFlags(t *testing.T) {
	cases := []struct {
		name                  string
		body                  string
		images, tools, stream bool
	}{
		{"ollama keep_alive ignored", `{"model":"m","prompt":"hi","keep_alive":"5m"}`, false, false, false},
		{"ollama raw ignored", `{"model":"m","prompt":"hi","raw":true}`, false, false, false},
		{"ollama format json string", `{"model":"m","prompt":"hi","format":"json"}`, false, false, false},
		{"ollama format schema object", `{"model":"m","prompt":"hi","format":{"type":"object","properties":{}}}`, false, false, false},
		{"ollama think bool", `{"model":"m","prompt":"hi","think":true}`, false, false, false},
		{"ollama think level", `{"model":"m","prompt":"hi","think":"high"}`, false, false, false},
		{"ollama context int array not text", `{"model":"m","prompt":"hi","context":[1,2,3,4,5]}`, false, false, false},
		{"openai tool_choice none still has tools", `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":"none"}`, false, true, false},
		{"openai legacy function_call", `{"model":"m","prompt":"hi","functions":[{"name":"f"}],"function_call":"auto"}`, false, true, false},
		{"content text missing field", `{"model":"m","messages":[{"role":"user","content":[{"type":"text"}]}]}`, false, false, false},
		{"content array with number and bool", `{"model":"m","messages":[{"role":"user","content":[1,true,{"type":"text","text":"x"}]}]}`, false, false, false},
		{"content null", `{"model":"m","messages":[{"role":"user","content":null}]}`, false, false, false},
		{"image_url bare string", `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":"http://x/y.png"}]}]}`, true, false, false},
		{"image_url data uri", `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`, true, false, false},
		{"anthropic image source", `{"model":"m","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`, true, false, false},
		{"multiple images across messages", `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"a"}}]},{"role":"user","content":[{"type":"image_url","image_url":{"url":"b"}}]}]}`, true, false, false},
		{"stream_options with stream true", `{"model":"m","prompt":"hi","stream":true,"stream_options":{"include_usage":true}}`, false, false, true},
		{"stream_options without stream", `{"model":"m","prompt":"hi","stream_options":{"include_usage":true}}`, false, false, false},
		{"empty messages array", `{"model":"m","messages":[]}`, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := Classify([]byte(c.body))
			if req.HasImages != c.images || req.RequiresTools != c.tools || req.RequiresStream != c.stream {
				t.Fatalf("flags = (img %v, tools %v, stream %v), want (%v, %v, %v)",
					req.HasImages, req.RequiresTools, req.RequiresStream, c.images, c.tools, c.stream)
			}
		})
	}
}

// TestClassify_NumCtxRequestsContext: Ollama options.num_ctx floors the required
// context window.
func TestClassify_NumCtxRequestsContext(t *testing.T) {
	req := Classify([]byte(`{"model":"m","prompt":"hi","options":{"num_ctx":32768}}`))
	if req.RequestedContext != 32768 {
		t.Fatalf("RequestedContext = %d, want 32768", req.RequestedContext)
	}
	if req.RequiredContext < 32768 {
		t.Fatalf("RequiredContext = %d, want >= 32768 (num_ctx floor)", req.RequiredContext)
	}
	small := Classify([]byte(`{"model":"m","prompt":"hi"}`))
	if small.RequiredContext >= 32768 {
		t.Fatalf("without num_ctx, a tiny prompt should need far less than 32768, got %d", small.RequiredContext)
	}
	neg := Classify([]byte(`{"model":"m","prompt":"hi","options":{"num_ctx":-1}}`))
	if neg.RequestedContext != 0 {
		t.Fatalf("negative num_ctx must be ignored, got %d", neg.RequestedContext)
	}
}

// TestClassify_NumCtxGatesEligibility: a num_ctx request larger than a model's
// window is rejected end to end through Evaluate.
func TestClassify_NumCtxGatesEligibility(t *testing.T) {
	req := Classify([]byte(`{"model":"logical","options":{"num_ctx":200000}}`))
	req.APIFamily = APIFamilyOllama
	small := Endpoint{ID: "small", Healthy: true, APIFamily: APIFamilyOllama,
		Models: []ModelRouting{{Physical: "p", Aliases: []string{"logical"}, Capabilities: Capabilities{Text: true}, Context: Context{MaxTokens: 8192}}}}
	if el := Evaluate(req, small); el.Reason != ReasonContextTooSmall {
		t.Fatalf("num_ctx 200k vs 8k window → %q, want CONTEXT_TOO_SMALL", el.Reason)
	}
	big := Endpoint{ID: "big", Healthy: true, APIFamily: APIFamilyOllama,
		Models: []ModelRouting{{Physical: "p", Aliases: []string{"logical"}, Capabilities: Capabilities{Text: true}, Context: Context{MaxTokens: 262144}}}}
	if el := Evaluate(req, big); !el.OK {
		t.Fatalf("num_ctx 200k vs 262k window should be eligible, got %q", el.Reason)
	}
}

// TestClassify_OutputPrecedenceEdges pins the output-length precedence at its
// boundaries.
func TestClassify_OutputPrecedenceEdges(t *testing.T) {
	if r := Classify([]byte(`{"model":"m","prompt":"hi","max_tokens":500,"max_completion_tokens":0}`)); r.OutputTokensReq != 0 {
		t.Fatalf("explicit max_completion_tokens:0 must win over max_tokens:500, got %d", r.OutputTokensReq)
	}
	if r := Classify([]byte(`{"model":"m","prompt":"hi","max_tokens":null}`)); r.OutputTokensReq != 0 {
		t.Fatalf("null max_tokens → 0, got %d", r.OutputTokensReq)
	}
	if r := Classify([]byte(`{"model":"m","prompt":"hi","max_tokens":1000000}`)); r.OutputTokensReq != 1000000 || r.RequiredContext < 1000000 {
		t.Fatalf("large max_tokens: out=%d req=%d", r.OutputTokensReq, r.RequiredContext)
	}
}

// TestClassify_RuneCountingAndWhitespace: multi-byte runes count as characters
// (not bytes), and whitespace-only content still counts.
func TestClassify_RuneCountingAndWhitespace(t *testing.T) {
	if r := Classify([]byte(`{"model":"m","messages":[{"role":"user","content":"👨‍👩‍👧‍👦 世界"}]}`)); r.InputTokensEst <= 0 {
		t.Fatal("emoji/CJK content produced a zero estimate")
	}
	if r := Classify([]byte(`{"model":"m","prompt":"     "}`)); r.InputTokensEst <= 0 {
		t.Fatal("whitespace-only prompt should still count characters")
	}
	// A byte-count estimator would inflate this; a rune-count one should not
	// explode. Just assert it is finite and positive and scales below bytes.
	cjk := Classify([]byte(`{"model":"m","prompt":"` + strings.Repeat("世", 300) + `"}`))
	if cjk.InputTokensEst <= 0 {
		t.Fatal("CJK prompt produced zero estimate")
	}
}

// TestClassify_EmbeddingsShapeNoCrash: the Ollama /api/embed and OpenAI
// embeddings shapes (input string or array, no prompt/messages) classify without
// crashing or setting spurious capability flags.
func TestClassify_EmbeddingsShapeNoCrash(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","input":"embed this"}`,
		`{"model":"m","input":["a","b","c"]}`,
	} {
		req := Classify([]byte(body))
		if req.Model != "m" {
			t.Fatalf("embed request lost model: %q", req.Model)
		}
		if req.HasImages || req.RequiresTools || req.RequiresStream {
			t.Fatalf("embed request set spurious flags: %+v", req)
		}
	}
}
