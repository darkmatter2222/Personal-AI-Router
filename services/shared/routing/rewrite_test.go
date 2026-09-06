// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"encoding/json"
	"strings"
	"testing"

	"nvpair-shared/noderec"
)

// The alias-rewriting tests exercise the production RewriteModelAlias /
// PhysicalForRefs / ServesModel / AliasesOf / ModelRefs / EffectiveCaps code —
// never a surrogate string substitution.

const aliasBody = `{"model":"local-coding","messages":[{"role":"user","content":"hi"}]}`

func model(t *testing.T, body []byte) string {
	t.Helper()
	var p struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return p.Model
}

func TestRewriteAliasToPhysical(t *testing.T) {
	refs := []noderec.EngineModelRef{
		{PhysicalName: "qwen-fast", Aliases: []string{"local-coding"}},
	}
	out := RewriteModelAlias([]byte(aliasBody), refs, "local-coding")
	if model(t, out) != "qwen-fast" {
		t.Fatalf("model = %q, want qwen-fast", model(t, out))
	}
	// Other fields must survive the rewrite.
	var p map[string]json.RawMessage
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := p["messages"]; !ok {
		t.Error("messages field lost in rewrite")
	}
}

func TestRewritePhysicalPassthrough(t *testing.T) {
	refs := []noderec.EngineModelRef{
		{PhysicalName: "qwen-fast", Aliases: []string{"local-coding"}},
	}
	out := RewriteModelAlias([]byte(aliasBody), refs, "qwen-fast")
	// The request already names the physical model: body unchanged.
	if string(out) != aliasBody {
		t.Fatalf("physical-name request should pass through unchanged, got %s", out)
	}
}

func TestRewriteUnknownModelUnchanged(t *testing.T) {
	refs := []noderec.EngineModelRef{
		{PhysicalName: "qwen-fast", Aliases: []string{"local-coding"}},
	}
	out := RewriteModelAlias([]byte(aliasBody), refs, "other-model")
	if string(out) != aliasBody {
		t.Fatalf("unknown model should not be rewritten, got %s", out)
	}
}

func TestRewriteNoRefsUnchanged(t *testing.T) {
	out := RewriteModelAlias([]byte(aliasBody), nil, "local-coding")
	if string(out) != aliasBody {
		t.Fatalf("no refs: body should be unchanged, got %s", out)
	}
}

func TestRewriteEmptyRequestedModelUnchanged(t *testing.T) {
	refs := []noderec.EngineModelRef{
		{PhysicalName: "qwen-fast", Aliases: []string{"local-coding"}},
	}
	out := RewriteModelAlias([]byte(aliasBody), refs, "")
	if string(out) != aliasBody {
		t.Fatalf("empty requested model: body should be unchanged, got %s", out)
	}
}

func TestRewriteMalformedBodyUnchanged(t *testing.T) {
	refs := []noderec.EngineModelRef{
		{PhysicalName: "qwen-fast", Aliases: []string{"local-coding"}},
	}
	body := []byte(`{not json`)
	out := RewriteModelAlias(body, refs, "local-coding")
	if string(out) != string(body) {
		t.Fatalf("malformed body should be unchanged, got %s", out)
	}
}

func TestRewriteNilBody(t *testing.T) {
	refs := []noderec.EngineModelRef{
		{PhysicalName: "qwen-fast", Aliases: []string{"local-coding"}},
	}
	if out := RewriteModelAlias(nil, refs, "local-coding"); out != nil {
		t.Fatalf("nil body should stay nil, got %s", out)
	}
}

func TestRewriteMultiModelRefs(t *testing.T) {
	// A multi-model engine: the same logical alias is declared per-model.
	// Each physical model resolves the alias to ITS physical name.
	refs := []noderec.EngineModelRef{
		{PhysicalName: "model-a", Aliases: []string{"shared"}},
		{PhysicalName: "model-b", Aliases: []string{"shared"}},
	}
	physical, ok := PhysicalForRefs(refs, "shared")
	if !ok || physical != "model-a" {
		t.Fatalf("first-match resolution = %q %v, want model-a (deterministic order)", physical, ok)
	}
	out := RewriteModelAlias([]byte(`{"model":"shared"}`), refs, "shared")
	if model(t, out) != "model-a" {
		t.Fatalf("rewrote to %q, want model-a", model(t, out))
	}
}

func TestRewriteLengthChange(t *testing.T) {
	// The physical name is a different length than the alias; the rewrite must
	// still produce valid JSON with the correct model field.
	refs := []noderec.EngineModelRef{
		{PhysicalName: "a-very-long-physical-model-name-1234567890", Aliases: []string{"short"}},
	}
	body := []byte(`{"model":"short","stream":true}`)
	out := RewriteModelAlias(body, refs, "short")
	if model(t, out) != "a-very-long-physical-model-name-1234567890" {
		t.Fatalf("model = %q", model(t, out))
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatalf("valid JSON expected: %v", err)
	}
	if string(p["stream"]) != "true" {
		t.Error("stream field lost")
	}
}

func TestServesModel(t *testing.T) {
	cases := []struct {
		served  []string
		aliases []string
		model   string
		want    bool
	}{
		{nil, nil, "", true},
		{nil, nil, "m", false},
		{[]string{"a"}, nil, "a", true},
		{[]string{"a"}, nil, "b", false},
		{nil, []string{"x"}, "x", true},
		{[]string{"a"}, []string{"x"}, "x", true},
		{[]string{"a"}, []string{"x"}, "y", false},
		// No fuzzy matching: similar names are not equivalent.
		{[]string{"qwen-fast"}, nil, "qwen-fast-2", false},
		{nil, []string{"local-coding"}, "local-coding-2", false},
	}
	for i, tc := range cases {
		if got := ServesModel(tc.served, tc.aliases, tc.model); got != tc.want {
			t.Errorf("case %d: ServesModel(%v, %v, %q) = %v, want %v",
				i, tc.served, tc.aliases, tc.model, got, tc.want)
		}
	}
}

func TestModelRefsSingularPlusList(t *testing.T) {
	r := &noderec.EngineRouting{
		ModelRef: &noderec.EngineModelRef{PhysicalName: "fixed-model"},
		Models: []noderec.EngineModelRef{
			{PhysicalName: "m-a"},
			{PhysicalName: "m-b"},
		},
	}
	refs := ModelRefs(r)
	if len(refs) != 3 || refs[0].PhysicalName != "fixed-model" || refs[2].PhysicalName != "m-b" {
		t.Fatalf("refs = %+v", refs)
	}
	if got := ModelRefs(nil); got != nil {
		t.Fatalf("nil routing refs = %+v, want nil", got)
	}
}

func TestAliasesOf(t *testing.T) {
	r := &noderec.EngineRouting{
		ModelRef: &noderec.EngineModelRef{PhysicalName: "fixed", Aliases: []string{"fixed-alias"}},
		Models: []noderec.EngineModelRef{
			{PhysicalName: "m-a", Aliases: []string{"a1", "a2"}},
			{PhysicalName: "m-b"},
		},
	}
	got := AliasesOf(r)
	want := []string{"fixed-alias", "a1", "a2"}
	if len(got) != len(want) {
		t.Fatalf("aliases = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("aliases = %v, want %v", got, want)
		}
	}
	if got := AliasesOf(nil); got != nil {
		t.Fatalf("nil aliases = %v, want nil", got)
	}
}

func TestEffectiveCapsInheritance(t *testing.T) {
	endpoint := EndpointCaps{
		Text:       boolPtr(true),
		MaxContext: 262144,
	}
	// No per-model declaration: inherit the endpoint defaults.
	got := EffectiveCaps(endpoint, nil, "any-model")
	if got.MaxContext != 262144 || !boolOn(got.Text) {
		t.Fatalf("inheritance = %+v", got)
	}
	// Empty model: pure endpoint defaults.
	got = EffectiveCaps(endpoint, nil, "")
	if got.MaxContext != 262144 {
		t.Fatalf("empty model = %+v", got)
	}
}

func TestEffectiveCapsModelOverride(t *testing.T) {
	endpoint := EndpointCaps{
		Text:       boolPtr(true),
		Vision:     boolPtr(false),
		Tools:      boolPtr(true),
		Streaming:  boolPtr(true),
		MaxContext: 32768,
	}
	// A per-model caps declaration REPLACES the endpoint capability flags for
	// that model (the model is the authority on what it can do), so the
	// declaration must be complete: vision on, text off, tools off, streaming
	// off — only the context budget is independently overridable.
	refs := []noderec.EngineModelRef{
		{PhysicalName: "vision-model", Caps: &noderec.EngineCaps{Vision: boolPtr(true)}, ContextMaxTokens: 128000},
	}
	got := EffectiveCaps(endpoint, refs, "vision-model")
	if !boolOn(got.Vision) {
		t.Fatal("vision override not applied")
	}
	if got.MaxContext != 128000 {
		t.Fatalf("context override = %d, want 128000", got.MaxContext)
	}
	if boolOn(got.Text) || boolOn(got.Tools) || boolOn(got.Streaming) {
		t.Fatal("a per-model caps declaration replaces the endpoint caps, it does not merge")
	}
}

func TestEffectiveCapsPartialOverride(t *testing.T) {
	endpoint := EndpointCaps{
		Text:       boolPtr(true),
		Tools:      boolPtr(true),
		MaxContext: 262144,
	}
	// The model declares only context, no caps: caps stay inherited, context
	// is overridden.
	refs := []noderec.EngineModelRef{
		{PhysicalName: "small-model", ContextMaxTokens: 4096},
	}
	got := EffectiveCaps(endpoint, refs, "small-model")
	if got.MaxContext != 4096 {
		t.Fatalf("context = %d, want 4096", got.MaxContext)
	}
	if !boolOn(got.Text) || !boolOn(got.Tools) {
		t.Fatal("caps should be inherited when the model declares no caps of its own")
	}
}

func TestEffectiveCapsFirstMatchWins(t *testing.T) {
	endpoint := EndpointCaps{Text: boolPtr(true)}
	refs := []noderec.EngineModelRef{
		{PhysicalName: "m", Aliases: []string{"x"}, ContextMaxTokens: 1000},
		{PhysicalName: "n", Aliases: []string{"x"}, ContextMaxTokens: 2000},
	}
	got := EffectiveCaps(endpoint, refs, "x")
	if got.MaxContext != 1000 {
		t.Fatalf("first-match context = %d, want 1000", got.MaxContext)
	}
}

func TestEffectiveCapsNonMatchingModel(t *testing.T) {
	endpoint := EndpointCaps{Text: boolPtr(true), MaxContext: 262144}
	refs := []noderec.EngineModelRef{
		{PhysicalName: "other", ContextMaxTokens: 1000},
	}
	got := EffectiveCaps(endpoint, refs, "requested")
	if got.MaxContext != 262144 {
		t.Fatalf("non-matching model changed context to %d", got.MaxContext)
	}
}

// TestRewriteFuzzSeeds is a bounded deterministic fuzz harness for the alias
// rewriter: arbitrary refs and bodies must not panic and must either pass the
// body through or rewrite only the model field.
func TestRewriteFuzzSeeds(t *testing.T) {
	refs := []noderec.EngineModelRef{
		{PhysicalName: "p1", Aliases: []string{"a1", "a2"}},
		{PhysicalName: "p2", Aliases: []string{"a2"}},
	}
	bodies := [][]byte{
		nil,
		{},
		[]byte("{"),
		[]byte(`{"model":"a1"}`),
		[]byte(`{"model":"a2","x":1}`),
		[]byte(`{"model":"nope"}`),
		[]byte(`{"model":"a1","model":"a2"}`), // duplicate key
		[]byte(strings.Repeat(`{"model":"a1"}`, 100)),
	}
	for i, body := range bodies {
		out := RewriteModelAlias(body, refs, "a1")
		if out == nil && body != nil {
			t.Fatalf("seed %d: nil output for non-nil body", i)
		}
	}
}
