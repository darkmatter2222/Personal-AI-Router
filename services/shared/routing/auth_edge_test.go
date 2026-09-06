// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"net/http"
	"testing"
)

func TestApplyAuth_MultipleDistinctHeaders(t *testing.T) {
	h := http.Header{}
	err := ApplyAuth(h, LocalAuth{Headers: []HeaderSpec{
		{Name: "Authorization", Value: "Bearer abc"},
		{Name: "X-Api-Key", ValueEnv: "K"},
	}}, fakeEnv(map[string]string{"K": "key123"}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if h.Get("Authorization") != "Bearer abc" || h.Get("X-Api-Key") != "key123" {
		t.Fatalf("both distinct headers should be applied: %v", h)
	}
}

func TestApplyAuth_PreservesUnrelatedHeaders(t *testing.T) {
	h := http.Header{"Content-Type": []string{"application/json"}, "Accept": []string{"*/*"}}
	if err := ApplyAuth(h, LocalAuth{Headers: []HeaderSpec{{Name: "Authorization", Value: "Bearer x"}}}, nil); err != nil {
		t.Fatalf("err: %v", err)
	}
	if h.Get("Content-Type") != "application/json" || h.Get("Accept") != "*/*" {
		t.Fatal("unrelated headers must be preserved when applying auth")
	}
}

func TestApplyAuth_ValueWithInternalSpaces(t *testing.T) {
	h := http.Header{}
	if err := ApplyAuth(h, LocalAuth{Headers: []HeaderSpec{{Name: "X-Token", Value: "abc def ghi"}}}, nil); err != nil {
		t.Fatalf("internal spaces are a valid header value: %v", err)
	}
	if h.Get("X-Token") != "abc def ghi" {
		t.Fatalf("value = %q", h.Get("X-Token"))
	}
}

func TestApplyAuth_SingleCharEnvValue(t *testing.T) {
	h := http.Header{}
	if err := ApplyAuth(h, LocalAuth{Headers: []HeaderSpec{{Name: "X-K", ValueEnv: "K"}}}, fakeEnv(map[string]string{"K": "x"})); err != nil {
		t.Fatalf("single-char env value is valid: %v", err)
	}
	if h.Get("X-K") != "x" {
		t.Fatal("single-char value lost")
	}
}
