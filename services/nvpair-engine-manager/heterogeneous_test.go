// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"strings"
	"testing"
)

// Manifest test matrix: valid/invalid combinations of the declarative routing
// fields (api_family, lifecycle, model, capabilities, context, routing,
// timeouts, http.headers) with random valid custom engine names.

func hetValidManifest() *Manifest {
	return &Manifest{
		Engine:          "custom-engine-98234",
		DisplayName:     "Custom Engine",
		ManifestVersion: 1,
		Platforms: map[string]Platform{
			"windows/amd64": {
				Runtime: Runtime{Mode: "command", Start: [][]string{{"run"}}},
			},
		},
	}
}

func TestManifestValidationMatrix(t *testing.T) {
	t.Run("valid minimal", func(t *testing.T) {
		if err := hetValidManifest().Validate(); err != nil {
			t.Fatalf("valid manifest rejected: %v", err)
		}
	})

	t.Run("missing engine", func(t *testing.T) {
		m := hetValidManifest()
		m.Engine = ""
		if err := m.Validate(); err == nil {
			t.Fatal("empty engine should fail")
		}
	})

	t.Run("invalid engine name", func(t *testing.T) {
		m := hetValidManifest()
		m.Engine = "bad name"
		if err := m.Validate(); err == nil {
			t.Fatal("invalid engine name should fail")
		}
	})

	t.Run("missing display name", func(t *testing.T) {
		m := hetValidManifest()
		m.DisplayName = ""
		if err := m.Validate(); err == nil {
			t.Fatal("missing display name should fail")
		}
	})

	t.Run("manifest version zero", func(t *testing.T) {
		m := hetValidManifest()
		m.ManifestVersion = 0
		if err := m.Validate(); err == nil {
			t.Fatal("version 0 should fail")
		}
	})

	t.Run("no platforms", func(t *testing.T) {
		m := hetValidManifest()
		m.Platforms = nil
		if err := m.Validate(); err == nil {
			t.Fatal("no platforms should fail")
		}
	})

	t.Run("lifecycle managed", func(t *testing.T) {
		m := hetValidManifest()
		m.Lifecycle = &LifecycleSpec{Mode: "managed"}
		if err := m.Validate(); err != nil {
			t.Fatalf("managed lifecycle rejected: %v", err)
		}
	})

	t.Run("lifecycle external", func(t *testing.T) {
		m := hetValidManifest()
		m.Lifecycle = &LifecycleSpec{Mode: "external"}
		if err := m.Validate(); err != nil {
			t.Fatalf("external lifecycle rejected: %v", err)
		}
	})

	t.Run("lifecycle omitted is valid", func(t *testing.T) {
		m := hetValidManifest()
		if err := m.Validate(); err != nil {
			t.Fatalf("omitted lifecycle rejected: %v", err)
		}
	})

	t.Run("lifecycle invalid mode", func(t *testing.T) {
		m := hetValidManifest()
		m.Lifecycle = &LifecycleSpec{Mode: "bogus"}
		if err := m.Validate(); err == nil {
			t.Fatal("invalid lifecycle mode should fail")
		}
	})

	t.Run("model with physical name", func(t *testing.T) {
		m := hetValidManifest()
		m.Model = &ManifestModel{PhysicalName: "actual-model", Aliases: []string{"local-coding"}}
		if err := m.Validate(); err != nil {
			t.Fatalf("model declaration rejected: %v", err)
		}
	})

	t.Run("model missing physical name", func(t *testing.T) {
		m := hetValidManifest()
		m.Model = &ManifestModel{Aliases: []string{"local-coding"}}
		if err := m.Validate(); err == nil {
			t.Fatal("missing physical name should fail")
		}
	})

	t.Run("model alias equals physical name", func(t *testing.T) {
		m := hetValidManifest()
		m.Model = &ManifestModel{PhysicalName: "phys", Aliases: []string{"phys"}}
		if err := m.Validate(); err == nil {
			t.Fatal("alias == physical name should fail")
		}
	})

	t.Run("model empty alias", func(t *testing.T) {
		m := hetValidManifest()
		m.Model = &ManifestModel{PhysicalName: "phys", Aliases: []string{""}}
		if err := m.Validate(); err == nil {
			t.Fatal("empty alias should fail")
		}
	})

	t.Run("context positive", func(t *testing.T) {
		m := hetValidManifest()
		m.Context = &ManifestContext{MaxTokens: 262144}
		if err := m.Validate(); err != nil {
			t.Fatalf("context rejected: %v", err)
		}
	})

	t.Run("context zero", func(t *testing.T) {
		m := hetValidManifest()
		m.Context = &ManifestContext{MaxTokens: 0}
		if err := m.Validate(); err == nil {
			t.Fatal("zero context should fail")
		}
	})

	t.Run("context negative", func(t *testing.T) {
		m := hetValidManifest()
		m.Context = &ManifestContext{MaxTokens: -1}
		if err := m.Validate(); err == nil {
			t.Fatal("negative context should fail")
		}
	})

	t.Run("routing valid", func(t *testing.T) {
		m := hetValidManifest()
		m.Routing = &ManifestRouting{Strategy: "deterministic", Priority: 10, StaticCapacity: 4, Pool: "pool-a"}
		if err := m.Validate(); err != nil {
			t.Fatalf("routing rejected: %v", err)
		}
	})

	t.Run("routing negative capacity", func(t *testing.T) {
		m := hetValidManifest()
		m.Routing = &ManifestRouting{StaticCapacity: -1}
		if err := m.Validate(); err == nil {
			t.Fatal("negative capacity should fail")
		}
	})

	t.Run("routing zero capacity is valid (unbounded)", func(t *testing.T) {
		m := hetValidManifest()
		m.Routing = &ManifestRouting{StaticCapacity: 0}
		if err := m.Validate(); err != nil {
			t.Fatalf("zero capacity should be valid (unbounded): %v", err)
		}
	})

	t.Run("routing invalid strategy", func(t *testing.T) {
		m := hetValidManifest()
		m.Routing = &ManifestRouting{Strategy: "bogus"}
		if err := m.Validate(); err == nil {
			t.Fatal("invalid strategy should fail")
		}
	})

	t.Run("routing omitted strategy is valid", func(t *testing.T) {
		m := hetValidManifest()
		m.Routing = &ManifestRouting{Priority: 10}
		if err := m.Validate(); err != nil {
			t.Fatalf("omitted strategy should be valid: %v", err)
		}
	})

	t.Run("timeouts valid", func(t *testing.T) {
		m := hetValidManifest()
		m.Timeouts = &ManifestTimeouts{ConnectMS: 250, ResponseHeaderMS: 30000, FirstByteMS: 60000}
		if err := m.Validate(); err != nil {
			t.Fatalf("timeouts rejected: %v", err)
		}
	})

	t.Run("timeouts negative", func(t *testing.T) {
		m := hetValidManifest()
		m.Timeouts = &ManifestTimeouts{ConnectMS: -1}
		if err := m.Validate(); err == nil {
			t.Fatal("negative timeout should fail")
		}
	})

	t.Run("http headers valid", func(t *testing.T) {
		m := hetValidManifest()
		m.HTTP = &ManifestHTTP{Headers: map[string]string{"Authorization": "Bearer ${MY_SECRET}"}}
		if err := m.Validate(); err != nil {
			t.Fatalf("http headers rejected: %v", err)
		}
	})

	t.Run("http headers empty name", func(t *testing.T) {
		m := hetValidManifest()
		m.HTTP = &ManifestHTTP{Headers: map[string]string{"": "value"}}
		if err := m.Validate(); err == nil {
			t.Fatal("empty header name should fail")
		}
	})

	t.Run("http headers empty value", func(t *testing.T) {
		m := hetValidManifest()
		m.HTTP = &ManifestHTTP{Headers: map[string]string{"Auth": ""}}
		if err := m.Validate(); err == nil {
			t.Fatal("empty header value should fail")
		}
	})

	t.Run("full heterogeneous manifest", func(t *testing.T) {
		m := hetValidManifest()
		m.APIFamily = "openai"
		m.Lifecycle = &LifecycleSpec{Mode: "external"}
		m.Model = &ManifestModel{PhysicalName: "actual-upstream-model", Aliases: []string{"local-coding"}}
		m.Capabilities = &EngineCapabilities{
			Text:      boolPtr(true),
			Vision:    boolPtr(false),
			Tools:     boolPtr(true),
			Streaming: boolPtr(true),
		}
		m.Context = &ManifestContext{MaxTokens: 262144}
		m.Routing = &ManifestRouting{Strategy: "deterministic", Priority: 10, StaticCapacity: 2}
		m.Timeouts = &ManifestTimeouts{ConnectMS: 250, ResponseHeaderMS: 30000, FirstByteMS: 30000}
		m.HTTP = &ManifestHTTP{Headers: map[string]string{"Authorization": "Bearer ${NVPAIR_VLLM_KEY}"}}
		if err := m.Validate(); err != nil {
			t.Fatalf("full heterogeneous manifest rejected: %v", err)
		}
	})
}

func boolPtr(b bool) *bool { return &b }

// buildRouting tests: manifest -> wire type conversion.

func TestBuildRoutingNil(t *testing.T) {
	m := hetValidManifest()
	if r := buildRouting(m); r != nil {
		t.Fatalf("no routing fields should produce nil, got %+v", r)
	}
}

func TestBuildRoutingFull(t *testing.T) {
	m := hetValidManifest()
	m.APIFamily = "openai"
	m.Lifecycle = &LifecycleSpec{Mode: "external"}
	m.Model = &ManifestModel{PhysicalName: "actual-model", Aliases: []string{"local-coding"}}
	m.Capabilities = &EngineCapabilities{
		Text:      boolPtr(true),
		Vision:    boolPtr(true),
		Tools:     boolPtr(true),
		Streaming: boolPtr(true),
		Reasoning: boolPtr(false),
	}
	m.Context = &ManifestContext{MaxTokens: 262144}
	m.Routing = &ManifestRouting{Strategy: "deterministic", Priority: 10, StaticCapacity: 4, Pool: "pool-a"}
	m.Timeouts = &ManifestTimeouts{ConnectMS: 250, ResponseHeaderMS: 30000, FirstByteMS: 60000}
	m.HTTP = &ManifestHTTP{Headers: map[string]string{"Authorization": "Bearer ${SECRET}"}}

	r := buildRouting(m)
	if r == nil {
		t.Fatal("buildRouting returned nil for a full manifest")
	}
	if r.APIFamily != "openai" {
		t.Errorf("api family = %q", r.APIFamily)
	}
	if r.ModelRef == nil || r.ModelRef.PhysicalName != "actual-model" || len(r.ModelRef.Aliases) != 1 {
		t.Errorf("model ref = %+v", r.ModelRef)
	}
	if r.Capabilities == nil || r.Capabilities.Text == nil || !*r.Capabilities.Text {
		t.Errorf("capabilities = %+v", r.Capabilities)
	}
	if r.ContextMaxTokens != 262144 {
		t.Errorf("context = %d", r.ContextMaxTokens)
	}
	if r.Strategy != "deterministic" || r.Priority != 10 || r.StaticCapacity != 4 || r.Pool != "pool-a" {
		t.Errorf("routing = %+v", r)
	}
	if r.Timeouts == nil || r.Timeouts.ConnectMS != 250 || r.Timeouts.ResponseHeaderMS != 30000 || r.Timeouts.FirstByteMS != 60000 {
		t.Errorf("timeouts = %+v", r.Timeouts)
	}
	if !r.AuthHeadersPresent {
		t.Error("auth headers present should be true")
	}
}

func TestBuildRoutingPartial(t *testing.T) {
	m := hetValidManifest()
	m.APIFamily = "openai"
	r := buildRouting(m)
	if r == nil {
		t.Fatal("api_family alone should produce non-nil routing")
	}
	if r.APIFamily != "openai" {
		t.Errorf("api family = %q", r.APIFamily)
	}
	if r.ModelRef != nil || r.Capabilities != nil || r.Timeouts != nil {
		t.Errorf("undeclared fields should be nil: %+v", r)
	}
	if r.AuthHeadersPresent {
		t.Error("no http headers should mean AuthHeadersPresent=false")
	}
}

func TestBuildRoutingRandomEngineNames(t *testing.T) {
	// Arbitrary engine IDs survive the manifest -> wire conversion without
	// any source-code enum.
	for _, name := range []string{"engine-test-1", "custom-openai-runtime", "my-vlm", "future-runtime-999"} {
		m := hetValidManifest()
		m.Engine = name
		m.APIFamily = "openai"
		r := buildRouting(m)
		if r == nil {
			t.Fatalf("engine %q: buildRouting returned nil", name)
		}
	}
}

// External lifecycle guard tests: an external engine must reject mutating
// operations while allowing read-only ones.

func TestExternalLifecycleGuards(t *testing.T) {
	m := hetValidManifest()
	m.Engine = "ext-engine-42"
	m.Lifecycle = &LifecycleSpec{Mode: "external"}
	ex := newTestExecutor(t, m)

	ctx := context.Background()
	cases := []struct {
		name string
		fn   func() error
	}{
		{"Install", func() error { return ex.Install(ctx, "ext-engine-42") }},
		{"Start", func() error { return ex.Start(ctx, "ext-engine-42") }},
		{"Stop", func() error { return ex.Stop("ext-engine-42") }},
		{"Restart", func() error { return ex.Restart(ctx, "ext-engine-42") }},
		{"Uninstall", func() error { return ex.Uninstall(ctx, "ext-engine-42") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			if err == nil {
				t.Fatalf("%s should fail for an external engine", tc.name)
			}
			if !strings.Contains(err.Error(), "externally managed") {
				t.Errorf("error = %q, want 'externally managed'", err.Error())
			}
		})
	}
}

func TestManagedLifecycleAllowsOperations(t *testing.T) {
	m := hetValidManifest()
	m.Engine = "managed-engine-42"
	m.Lifecycle = &LifecycleSpec{Mode: "managed"}
	ex := newTestExecutor(t, m)

	// A managed engine should NOT get the "externally managed" error from
	// these operations (it may fail for other reasons like "not installed",
	// but not from the lifecycle guard).
	err := ex.Stop("managed-engine-42")
	if err != nil && strings.Contains(err.Error(), "externally managed") {
		t.Errorf("managed engine got external guard error: %v", err)
	}
}

func TestExternalLifecycleModelOps(t *testing.T) {
	m := hetValidManifest()
	m.Engine = "ext-model-ops"
	m.Lifecycle = &LifecycleSpec{Mode: "external"}
	ex := newTestExecutor(t, m)

	ctx := context.Background()
	if _, err := ex.ModelLoad(ctx, "ext-model-ops", "some-model"); err == nil || !strings.Contains(err.Error(), "externally managed") {
		t.Errorf("ModelLoad: %v", err)
	}
	if _, err := ex.ModelUnload(ctx, "ext-model-ops", "some-model"); err == nil || !strings.Contains(err.Error(), "externally managed") {
		t.Errorf("ModelUnload: %v", err)
	}
	if _, err := ex.ModelDelete(ctx, "ext-model-ops", "some-model"); err == nil || !strings.Contains(err.Error(), "externally managed") {
		t.Errorf("ModelDelete: %v", err)
	}
	if _, err := ex.PullModelStream(ctx, "ext-model-ops", "some-model", nil); err == nil || !strings.Contains(err.Error(), "externally managed") {
		t.Errorf("PullModelStream: %v", err)
	}
}

// isExternal tests.

func TestIsExternal(t *testing.T) {
	if (&Manifest{}).isExternal() {
		t.Error("nil lifecycle should not be external")
	}
	if (&Manifest{Lifecycle: &LifecycleSpec{Mode: "managed"}}).isExternal() {
		t.Error("managed should not be external")
	}
	if !(&Manifest{Lifecycle: &LifecycleSpec{Mode: "external"}}).isExternal() {
		t.Error("external should be external")
	}
	if (&Manifest{Lifecycle: &LifecycleSpec{Mode: ""}}).isExternal() {
		t.Error("empty mode should not be external")
	}
}
