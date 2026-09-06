// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"strings"
	"testing"
)

// TestValidate_ModelNameLengthBoundary: a 512-char name is accepted, 513 is not.
func TestValidate_ModelNameLengthBoundary(t *testing.T) {
	if err := (EngineRouting{Models: []ModelRouting{{Physical: strings.Repeat("a", 512)}}}).Validate(); err != nil {
		t.Fatalf("512-char physical name should be valid: %v", err)
	}
	if err := (EngineRouting{Models: []ModelRouting{{Physical: strings.Repeat("a", 513)}}}).Validate(); err == nil {
		t.Fatal("513-char physical name should be rejected")
	}
}

// TestValidate_AliasNamespaceRules: within one endpoint the physical-name and
// alias namespace must be unique. Duplicate aliases (across or within models),
// alias/physical collisions, and duplicate physical names are all ambiguous and
// rejected. Valid names may contain '/'/':' but not control characters.
func TestValidate_AliasNamespaceRules(t *testing.T) {
	cases := []struct {
		name    string
		er      EngineRouting
		wantErr bool
	}{
		{"distinct namespace ok", EngineRouting{Models: []ModelRouting{
			{Physical: "qwen-fast", Aliases: []string{"local-coding", "coder"}},
			{Physical: "qwen-q4", Aliases: []string{"fast-coding"}},
		}}, false},
		{"slash/colon alias ok", EngineRouting{Models: []ModelRouting{{Physical: "p", Aliases: []string{"repo/m:tag"}}}}, false},
		{"duplicate alias across models rejected", EngineRouting{Models: []ModelRouting{
			{Physical: "a", Aliases: []string{"x"}},
			{Physical: "b", Aliases: []string{"x"}},
		}}, true},
		{"duplicate alias within one model rejected", EngineRouting{Models: []ModelRouting{
			{Physical: "a", Aliases: []string{"x", "x"}},
		}}, true},
		{"alias collides with another physical rejected", EngineRouting{Models: []ModelRouting{
			{Physical: "foo"},
			{Physical: "bar", Aliases: []string{"foo"}},
		}}, true},
		{"alias equals own physical rejected", EngineRouting{Models: []ModelRouting{
			{Physical: "foo", Aliases: []string{"foo"}},
		}}, true},
		{"duplicate physical rejected", EngineRouting{Models: []ModelRouting{{Physical: "dup"}, {Physical: "dup"}}}, true},
		{"control char alias rejected", EngineRouting{Models: []ModelRouting{{Physical: "p", Aliases: []string{"a\tb"}}}}, true},
		{"empty alias rejected", EngineRouting{Models: []ModelRouting{{Physical: "p", Aliases: []string{""}}}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.er.Validate(); (err != nil) != c.wantErr {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, c.wantErr)
			}
		})
	}
}

// TestValidate_SameAliasAcrossEndpointsValid: the same logical alias mapping to
// different physical models on SEPARATE endpoints is the heterogeneous
// abstraction and must validate on each endpoint independently.
func TestValidate_SameAliasAcrossEndpointsValid(t *testing.T) {
	a := EngineRouting{Models: []ModelRouting{{Physical: "qwen-fast", Aliases: []string{"local-coding"}}}}
	b := EngineRouting{Models: []ModelRouting{{Physical: "qwen-q4", Aliases: []string{"local-coding"}}}}
	c := EngineRouting{Models: []ModelRouting{{Physical: "flash-next", Aliases: []string{"local-coding"}}}}
	for i, er := range []EngineRouting{a, b, c} {
		if err := er.Validate(); err != nil {
			t.Fatalf("endpoint %d with shared alias should validate: %v", i, err)
		}
	}
}

// TestValidate_HealthPath: a set health path must start with '/'.
func TestValidate_HealthPath(t *testing.T) {
	if err := (EngineRouting{Health: Health{Path: "/health"}}).Validate(); err != nil {
		t.Fatalf("/health should be valid: %v", err)
	}
	if err := (EngineRouting{Health: Health{Path: "health"}}).Validate(); err == nil {
		t.Fatal("health path without leading slash should be rejected")
	}
	if err := (EngineRouting{Health: Health{Path: "/ok\n"}}).Validate(); err == nil {
		t.Fatal("health path with control char should be rejected")
	}
	if err := (EngineRouting{}).Validate(); err != nil {
		t.Fatalf("empty health path should be fine: %v", err)
	}
}
