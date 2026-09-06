// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import "testing"

// fullCaps is a model that supports everything with a large window.
func capModel(physical string, c Capabilities, maxTok int, aliases ...string) ModelRouting {
	return ModelRouting{Physical: physical, Aliases: aliases, Capabilities: c, Context: Context{MaxTokens: maxTok}}
}

func healthyEndpoint(models ...ModelRouting) Endpoint {
	return Endpoint{ID: "n1", Engine: "e", APIFamily: APIFamilyOpenAI, Healthy: true, Models: models}
}

func TestEvaluate_OK(t *testing.T) {
	e := healthyEndpoint(capModel("m", Capabilities{Text: true, Vision: true, Tools: true, Streaming: true}, 100000, "logical"))
	req := Requirements{Model: "logical", APIFamily: APIFamilyOpenAI, HasImages: true, RequiresTools: true, RequiresStream: true, RequiredContext: 500}
	el := Evaluate(req, e)
	if !el.OK {
		t.Fatalf("expected OK, got reason %q", el.Reason)
	}
	if el.Model.Physical != "m" {
		t.Fatalf("resolved model physical = %q", el.Model.Physical)
	}
}

func TestEvaluate_Gates(t *testing.T) {
	base := Capabilities{Text: true, Vision: true, Tools: true, Streaming: true}
	cases := []struct {
		name   string
		ep     Endpoint
		req    Requirements
		want   Reason
	}{
		{
			"model not available",
			healthyEndpoint(capModel("m", base, 0)),
			Requirements{Model: "other"},
			ReasonModelNotAvailable,
		},
		{
			"api family incompatible",
			Endpoint{ID: "n", Healthy: true, APIFamily: APIFamilyOllama, Models: []ModelRouting{capModel("m", base, 0)}},
			Requirements{Model: "m", APIFamily: APIFamilyOpenAI},
			ReasonAPIFamilyIncompatible,
		},
		{
			"family skipped when endpoint unknown",
			Endpoint{ID: "n", Healthy: true, APIFamily: APIFamilyUnknown, Models: []ModelRouting{capModel("m", base, 0)}},
			Requirements{Model: "m", APIFamily: APIFamilyOpenAI},
			ReasonNone,
		},
		{
			"family skipped when req unknown",
			Endpoint{ID: "n", Healthy: true, APIFamily: APIFamilyOpenAI, Models: []ModelRouting{capModel("m", base, 0)}},
			Requirements{Model: "m"},
			ReasonNone,
		},
		{
			"text required",
			healthyEndpoint(capModel("m", Capabilities{Text: false, Vision: true}, 0)),
			Requirements{Model: "m", APIFamily: APIFamilyOpenAI},
			ReasonTextRequired,
		},
		{
			"vision required",
			healthyEndpoint(capModel("m", Capabilities{Text: true, Vision: false}, 0)),
			Requirements{Model: "m", APIFamily: APIFamilyOpenAI, HasImages: true},
			ReasonVisionRequired,
		},
		{
			"vision ok",
			healthyEndpoint(capModel("m", Capabilities{Text: true, Vision: true}, 0)),
			Requirements{Model: "m", APIFamily: APIFamilyOpenAI, HasImages: true},
			ReasonNone,
		},
		{
			"tools required",
			healthyEndpoint(capModel("m", Capabilities{Text: true, Tools: false}, 0)),
			Requirements{Model: "m", APIFamily: APIFamilyOpenAI, RequiresTools: true},
			ReasonToolsRequired,
		},
		{
			"streaming required",
			healthyEndpoint(capModel("m", Capabilities{Text: true, Streaming: false}, 0)),
			Requirements{Model: "m", APIFamily: APIFamilyOpenAI, RequiresStream: true},
			ReasonStreamingRequired,
		},
		{
			"context too small",
			healthyEndpoint(capModel("m", Capabilities{Text: true}, 1024)),
			Requirements{Model: "m", APIFamily: APIFamilyOpenAI, RequiredContext: 2048},
			ReasonContextTooSmall,
		},
		{
			"context ok within window",
			healthyEndpoint(capModel("m", Capabilities{Text: true}, 4096)),
			Requirements{Model: "m", APIFamily: APIFamilyOpenAI, RequiredContext: 2048},
			ReasonNone,
		},
		{
			"context ok when undeclared (0)",
			healthyEndpoint(capModel("m", Capabilities{Text: true}, 0)),
			Requirements{Model: "m", APIFamily: APIFamilyOpenAI, RequiredContext: 9_000_000},
			ReasonNone,
		},
		{
			"context boundary equal ok",
			healthyEndpoint(capModel("m", Capabilities{Text: true}, 2048)),
			Requirements{Model: "m", APIFamily: APIFamilyOpenAI, RequiredContext: 2048},
			ReasonNone,
		},
		{
			"disabled",
			Endpoint{ID: "n", Healthy: true, Disabled: true, APIFamily: APIFamilyOpenAI, Models: []ModelRouting{capModel("m", Capabilities{Text: true}, 0)}},
			Requirements{Model: "m", APIFamily: APIFamilyOpenAI},
			ReasonEndpointDisabled,
		},
		{
			"unhealthy",
			Endpoint{ID: "n", Healthy: false, APIFamily: APIFamilyOpenAI, Models: []ModelRouting{capModel("m", Capabilities{Text: true}, 0)}},
			Requirements{Model: "m", APIFamily: APIFamilyOpenAI},
			ReasonEndpointUnhealthy,
		},
		{
			"draining",
			Endpoint{ID: "n", Healthy: true, Draining: true, APIFamily: APIFamilyOpenAI, Models: []ModelRouting{capModel("m", Capabilities{Text: true}, 0)}},
			Requirements{Model: "m", APIFamily: APIFamilyOpenAI},
			ReasonEndpointDraining,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			el := Evaluate(c.req, c.ep)
			if c.want == ReasonNone {
				if !el.OK {
					t.Fatalf("expected OK, got %q", el.Reason)
				}
				return
			}
			if el.OK || el.Reason != c.want {
				t.Fatalf("reason = %q (ok=%v), want %q", el.Reason, el.OK, c.want)
			}
		})
	}
}

func TestEvaluate_ReasonPrecedence(t *testing.T) {
	base := Capabilities{Text: true}
	// Model checked before state: a disabled endpoint that lacks the model
	// reports MODEL_NOT_AVAILABLE (the reason is never lost to an earlier state
	// filter).
	e := Endpoint{ID: "n", Disabled: true, Healthy: false, APIFamily: APIFamilyOpenAI, Models: []ModelRouting{capModel("m", base, 0)}}
	if el := Evaluate(Requirements{Model: "absent"}, e); el.Reason != ReasonModelNotAvailable {
		t.Fatalf("want MODEL_NOT_AVAILABLE, got %q", el.Reason)
	}
	// Capabilities checked before state: a text=false model on a disabled
	// endpoint reports TEXT_REQUIRED.
	e2 := Endpoint{ID: "n", Disabled: true, Healthy: true, APIFamily: APIFamilyOpenAI, Models: []ModelRouting{capModel("m", Capabilities{Text: false}, 0)}}
	if el := Evaluate(Requirements{Model: "m", APIFamily: APIFamilyOpenAI}, e2); el.Reason != ReasonTextRequired {
		t.Fatalf("want TEXT_REQUIRED, got %q", el.Reason)
	}
	// Unhealthy checked before draining.
	e3 := Endpoint{ID: "n", Healthy: false, Draining: true, APIFamily: APIFamilyOpenAI, Models: []ModelRouting{capModel("m", base, 0)}}
	if el := Evaluate(Requirements{Model: "m", APIFamily: APIFamilyOpenAI}, e3); el.Reason != ReasonEndpointUnhealthy {
		t.Fatalf("want ENDPOINT_UNHEALTHY, got %q", el.Reason)
	}
}

func TestEvaluate_RejectionCarriesModel(t *testing.T) {
	// A non-model rejection still reports which model it referred to.
	e := healthyEndpoint(capModel("physical-x", Capabilities{Text: true, Vision: false}, 0, "logical-x"))
	el := Evaluate(Requirements{Model: "logical-x", APIFamily: APIFamilyOpenAI, HasImages: true}, e)
	if el.Reason != ReasonVisionRequired {
		t.Fatalf("reason = %q", el.Reason)
	}
	if el.Model.Physical != "physical-x" {
		t.Fatalf("rejection lost model, physical = %q", el.Model.Physical)
	}
}
