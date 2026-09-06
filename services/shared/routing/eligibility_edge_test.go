// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import "testing"

// TestEvaluate_MultiModelResolvesMatchedModelCaps exercises a multi-model engine
// (one endpoint serving models with genuinely different capabilities and context
// windows): the gate always uses the capabilities and context of the model the
// requested name actually resolves to, never another model's.
func TestEvaluate_MultiModelResolvesMatchedModelCaps(t *testing.T) {
	e := Endpoint{ID: "n", Healthy: true, APIFamily: APIFamilyOpenAI, Models: []ModelRouting{
		{Physical: "text-32k", Aliases: []string{"fast"}, Capabilities: Capabilities{Text: true}, Context: Context{MaxTokens: 32768}},
		{Physical: "vision-128k", Aliases: []string{"see"}, Capabilities: Capabilities{Text: true, Vision: true}, Context: Context{MaxTokens: 131072}},
		{Physical: "tools-262k", Aliases: []string{"agent"}, Capabilities: Capabilities{Text: true, Tools: true}, Context: Context{MaxTokens: 262144}},
	}}

	if el := Evaluate(Requirements{Model: "see", APIFamily: APIFamilyOpenAI, HasImages: true}, e); !el.OK || el.Model.Physical != "vision-128k" {
		t.Fatalf("vision alias should resolve the vision model: ok=%v model=%q reason=%q", el.OK, el.Model.Physical, el.Reason)
	}
	if el := Evaluate(Requirements{Model: "fast", APIFamily: APIFamilyOpenAI, HasImages: true}, e); el.Reason != ReasonVisionRequired {
		t.Fatalf("vision request to the text model → %q, want VISION_REQUIRED", el.Reason)
	}
	if el := Evaluate(Requirements{Model: "agent", APIFamily: APIFamilyOpenAI, RequiresTools: true}, e); !el.OK || el.Model.Physical != "tools-262k" {
		t.Fatalf("tools alias should resolve the tools model, got %+v", el)
	}
	if el := Evaluate(Requirements{Model: "fast", APIFamily: APIFamilyOpenAI, RequiredContext: 50000}, e); el.Reason != ReasonContextTooSmall {
		t.Fatalf("50k vs fast(32k) → %q, want CONTEXT_TOO_SMALL", el.Reason)
	}
	if el := Evaluate(Requirements{Model: "agent", APIFamily: APIFamilyOpenAI, RequiredContext: 50000}, e); !el.OK {
		t.Fatalf("50k vs agent(262k) should pass, got %q", el.Reason)
	}
}

func TestEvaluate_OllamaFamilyMatches(t *testing.T) {
	e := Endpoint{ID: "n", Healthy: true, APIFamily: APIFamilyOllama,
		Models: []ModelRouting{{Physical: "m", Capabilities: Capabilities{Text: true}}}}
	if el := Evaluate(Requirements{Model: "m", APIFamily: APIFamilyOllama}, e); !el.OK {
		t.Fatalf("ollama↔ollama should be eligible, got %q", el.Reason)
	}
}

func TestEvaluate_EmptyModelsIsNotAvailable(t *testing.T) {
	e := Endpoint{ID: "n", Healthy: true, APIFamily: APIFamilyOpenAI}
	if el := Evaluate(Requirements{Model: "anything", APIFamily: APIFamilyOpenAI}, e); el.Reason != ReasonModelNotAvailable {
		t.Fatalf("endpoint with no models → %q, want MODEL_NOT_AVAILABLE", el.Reason)
	}
}

// TestEvaluate_MetadataOnlyCapsDoNotGate: reasoning/embedding/audio are declared
// but not enforced, so a model that lacks them still serves a normal request.
func TestEvaluate_MetadataOnlyCapsDoNotGate(t *testing.T) {
	e := Endpoint{ID: "n", Healthy: true, APIFamily: APIFamilyOpenAI, Models: []ModelRouting{
		{Physical: "m", Capabilities: Capabilities{Text: true, Reasoning: false, Embedding: false, Audio: false}},
	}}
	if el := Evaluate(Requirements{Model: "m", APIFamily: APIFamilyOpenAI}, e); !el.OK {
		t.Fatalf("metadata-only caps must not gate, got %q", el.Reason)
	}
}

// TestEvaluate_UnneededCapabilitiesDoNotGate: a model lacking vision/tools/
// streaming is still eligible when the request needs none of them.
func TestEvaluate_UnneededCapabilitiesDoNotGate(t *testing.T) {
	e := Endpoint{ID: "n", Healthy: true, APIFamily: APIFamilyOpenAI, Models: []ModelRouting{
		{Physical: "m", Capabilities: Capabilities{Text: true, Vision: false, Tools: false, Streaming: false}},
	}}
	if el := Evaluate(Requirements{Model: "m", APIFamily: APIFamilyOpenAI}, e); !el.OK {
		t.Fatalf("unneeded caps must not gate, got %q", el.Reason)
	}
}
