// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"nvpair-shared/noderec"
)

// Timeout resolution: every field independently, with the documented
// omitted/zero = default semantics.

func TestResolveTimeoutsNilIsDefault(t *testing.T) {
	p := ResolveTimeouts(nil)
	if p.Connect != DefaultConnectTimeout ||
		p.ResponseHeader != DefaultResponseHeaderTimeout ||
		p.FirstByte != DefaultFirstByteTimeout {
		t.Fatalf("nil timeouts = %+v, want all defaults", p)
	}
}

func TestResolveTimeoutsZeroFieldsAreDefault(t *testing.T) {
	p := ResolveTimeouts(&noderec.EngineTimeouts{})
	if p.Connect != DefaultConnectTimeout ||
		p.ResponseHeader != DefaultResponseHeaderTimeout ||
		p.FirstByte != DefaultFirstByteTimeout {
		t.Fatalf("zero timeouts = %+v, want all defaults", p)
	}
}

func TestResolveTimeoutsNegativeFieldsAreDefault(t *testing.T) {
	p := ResolveTimeouts(&noderec.EngineTimeouts{ConnectMS: -1, ResponseHeaderMS: -2, FirstByteMS: -3})
	if p.Connect != DefaultConnectTimeout ||
		p.ResponseHeader != DefaultResponseHeaderTimeout ||
		p.FirstByte != DefaultFirstByteTimeout {
		t.Fatalf("negative timeouts = %+v, want all defaults", p)
	}
}

func TestResolveTimeoutsPerFieldOverride(t *testing.T) {
	p := ResolveTimeouts(&noderec.EngineTimeouts{ConnectMS: 250, ResponseHeaderMS: 30000, FirstByteMS: 60000})
	if p.Connect != 250*time.Millisecond {
		t.Errorf("connect = %v, want 250ms", p.Connect)
	}
	if p.ResponseHeader != 30*time.Second {
		t.Errorf("response header = %v, want 30s", p.ResponseHeader)
	}
	if p.FirstByte != 60*time.Second {
		t.Errorf("first byte = %v, want 60s", p.FirstByte)
	}
}

func TestResolveTimeoutsPartialOverride(t *testing.T) {
	// One endpoint's long first-byte budget must not weaken its own connect
	// budget, and other fields keep their defaults.
	p := ResolveTimeouts(&noderec.EngineTimeouts{FirstByteMS: 300000})
	if p.FirstByte != 5*time.Minute {
		t.Errorf("first byte = %v, want 5m", p.FirstByte)
	}
	if p.Connect != DefaultConnectTimeout {
		t.Errorf("connect should stay default, got %v", p.Connect)
	}
	if p.ResponseHeader != DefaultResponseHeaderTimeout {
		t.Errorf("response header should stay default, got %v", p.ResponseHeader)
	}
}

// Auth header resolution and application through the production path.

func TestResolveSecretValueLiteral(t *testing.T) {
	got, err := ResolveSecretValue("Bearer literal-token")
	if err != nil || got != "Bearer literal-token" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestResolveSecretValueEnv(t *testing.T) {
	t.Setenv("PAIR_TEST_SECRET", "s3cret-value")
	got, err := ResolveSecretValue("${PAIR_TEST_SECRET}")
	if err != nil || got != "s3cret-value" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestResolveSecretValueEnvMissing(t *testing.T) {
	if _, v := os.LookupEnv("PAIR_TEST_MISSING"); v {
		t.Setenv("PAIR_TEST_MISSING", "")
		_ = os.Unsetenv("PAIR_TEST_MISSING")
	}
	_, err := ResolveSecretValue("${PAIR_TEST_MISSING}")
	if err == nil {
		t.Fatal("missing env var must fail")
	}
	var se *SecretError
	if !errors.As(err, &se) {
		t.Fatalf("want SecretError, got %T", err)
	}
	if se.Msg != "env variable PAIR_TEST_MISSING is not set" {
		t.Fatalf("error names the variable but not its value: %q", se.Msg)
	}
}

func TestResolveSecretValueEnvEmpty(t *testing.T) {
	t.Setenv("PAIR_TEST_EMPTY", "")
	if _, err := ResolveSecretValue("${PAIR_TEST_EMPTY}"); err == nil {
		t.Fatal("empty env var must fail (an empty credential is not a credential)")
	}
}

func TestResolveSecretValueMixedIsMalformed(t *testing.T) {
	t.Setenv("PAIR_TEST_X", "v")
	for _, v := range []string{
		"Bearer ${PAIR_TEST_X}", // env mixed into literal text
		"${PAIR_TEST_X} extra",
		"a${PAIR_TEST_X}b",
	} {
		if _, err := ResolveSecretValue(v); err == nil {
			t.Errorf("mixed value %q should be malformed", v)
		}
	}
}

func TestResolveSecretValueMalformedRefs(t *testing.T) {
	// No ${ in the value: it is a literal, not a malformed reference.
	for _, v := range []string{"$", "a}", "bare"} {
		if got, err := ResolveSecretValue(v); err != nil || got != v {
			t.Errorf("literal %q must pass through, got %q %v", v, got, err)
		}
	}
	// A ${ present but not a clean single reference: malformed.
	for _, v := range []string{"${}", "${a", "${a}b}", "${a}${b}"} {
		if _, err := ResolveSecretValue(v); err == nil {
			t.Errorf("malformed ref %q should fail", v)
		}
	}
}

func TestResolveSecretValueEmpty(t *testing.T) {
	if _, err := ResolveSecretValue(""); err == nil {
		t.Fatal("empty value should fail")
	}
}

func TestValidateHeaderName(t *testing.T) {
	valid := []string{"Authorization", "X-Custom-Header", "x", "A-B_C"}
	for _, name := range valid {
		if err := ValidateHeaderName(name); err != nil {
			t.Errorf("valid name %q rejected: %v", name, err)
		}
	}
	invalid := []string{"", "Bad Name", "Line\nBreak", "Tab\tName", "Semicolon;", "With:Colon"}
	for _, name := range invalid {
		if err := ValidateHeaderName(name); err == nil {
			t.Errorf("invalid name %q accepted", name)
		}
	}
}

func TestValidateHeaderValue(t *testing.T) {
	if err := ValidateHeaderValue("ok value"); err != nil {
		t.Fatalf("valid value rejected: %v", err)
	}
	for _, v := range []string{"", "bad\nvalue", "bad\rvalue", "a\rb\nc"} {
		if err := ValidateHeaderValue(v); err == nil {
			t.Errorf("newline-injecting value %q accepted", v)
		}
	}
}

func TestResolveHeadersMatrix(t *testing.T) {
	t.Setenv("PAIR_TEST_AUTH", "tok-123")
	headers, err := ResolveHeaders(map[string]string{
		"Authorization": "${PAIR_TEST_AUTH}",
		"X-Static":      "plain",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if headers["Authorization"] != "tok-123" || headers["X-Static"] != "plain" {
		t.Fatalf("headers = %v", headers)
	}

	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"malformed name", map[string]string{"Bad Name": "v"}},
		{"newline in name", map[string]string{"Bad\nName": "v"}},
		{"empty value", map[string]string{"Auth": ""}},
		{"missing env", map[string]string{"Auth": "${PAIR_TEST_NOPE}"}},
		{"empty env", map[string]string{"Auth": "${PAIR_TEST_EMPTY2}"}},
		{"newline in env value", map[string]string{"Auth": "${PAIR_TEST_NL}"}},
	}
	if v, ok := os.LookupEnv("PAIR_TEST_NL"); ok && v != "a\nb" {
		t.Setenv("PAIR_TEST_NL", "a\nb")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "empty env" {
				t.Setenv("PAIR_TEST_EMPTY2", "")
			}
			if _, err := ResolveHeaders(tc.headers); err == nil {
				t.Fatal("should fail")
			}
		})
	}
}

func TestResolveHeadersEmptyMap(t *testing.T) {
	got, err := ResolveHeaders(nil)
	if err != nil || got != nil {
		t.Fatalf("nil map = %v, %v; want nil, nil", got, err)
	}
	got, err = ResolveHeaders(map[string]string{})
	if err != nil || got != nil {
		t.Fatalf("empty map = %v, %v; want nil, nil", got, err)
	}
}

func TestApplyAuthHeadersThroughProductionRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:1234/v1/chat/completions", nil)
	headers := map[string]string{"Authorization": "Bearer tok-abc", "X-Extra": "v"}
	ApplyAuthHeaders(req, headers)
	if got := req.Header.Get("Authorization"); got != "Bearer tok-abc" {
		t.Fatalf("authorization header = %q", got)
	}
	if got := req.Header.Get("X-Extra"); got != "v" {
		t.Fatalf("extra header = %q", got)
	}
	// Empty values are skipped, not set.
	ApplyAuthHeaders(req, map[string]string{"X-Empty": ""})
	if got := req.Header.Get("X-Empty"); got != "" {
		t.Fatalf("empty header value should be skipped, got %q", got)
	}
}

func TestApplyAuthHeadersOverwritesExisting(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:1234/", nil)
	req.Header.Set("Authorization", "client-header")
	ApplyAuthHeaders(req, map[string]string{"Authorization": "backend-secret"})
	if got := req.Header.Get("Authorization"); got != "backend-secret" {
		t.Fatalf("auth header should be set by PAIR, got %q", got)
	}
}
