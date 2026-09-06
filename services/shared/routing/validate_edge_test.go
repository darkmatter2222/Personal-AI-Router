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

// TestValidate_AliasRules: a duplicate alias across models is allowed (only
// duplicate physical names are rejected); aliases may contain '/'/':' but not
// control characters.
func TestValidate_AliasRules(t *testing.T) {
	dupAlias := EngineRouting{Models: []ModelRouting{
		{Physical: "a", Aliases: []string{"x"}},
		{Physical: "b", Aliases: []string{"x"}},
	}}
	if err := dupAlias.Validate(); err != nil {
		t.Fatalf("duplicate alias across models should be allowed: %v", err)
	}
	if err := (EngineRouting{Models: []ModelRouting{{Physical: "p", Aliases: []string{"repo/m:tag"}}}}).Validate(); err != nil {
		t.Fatalf("slash/colon alias should be valid: %v", err)
	}
	if err := (EngineRouting{Models: []ModelRouting{{Physical: "p", Aliases: []string{"a\tb"}}}}).Validate(); err == nil {
		t.Fatal("tab (control char) in alias should be rejected")
	}
	if err := (EngineRouting{Models: []ModelRouting{{Physical: "dup"}, {Physical: "dup"}}}).Validate(); err == nil {
		t.Fatal("duplicate physical name should be rejected")
	}
}
