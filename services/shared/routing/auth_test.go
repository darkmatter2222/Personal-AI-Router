// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
)

func fakeEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestApplyAuth_LiteralValue(t *testing.T) {
	h := http.Header{}
	err := ApplyAuth(h, LocalAuth{Headers: []HeaderSpec{{Name: "Authorization", Value: "Bearer abc123"}}}, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := h.Get("Authorization"); got != "Bearer abc123" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestApplyAuth_EnvValue(t *testing.T) {
	h := http.Header{}
	env := fakeEnv(map[string]string{"MY_TOKEN": "Bearer from-env"})
	err := ApplyAuth(h, LocalAuth{Headers: []HeaderSpec{{Name: "Authorization", ValueEnv: "MY_TOKEN"}}}, env)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := h.Get("Authorization"); got != "Bearer from-env" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestApplyAuth_RealEnvViaSetenv(t *testing.T) {
	t.Setenv("PAIR_TEST_TOKEN", "Bearer real-env")
	h := http.Header{}
	if err := ApplyAuth(h, LocalAuth{Headers: []HeaderSpec{{Name: "X-Api-Key", ValueEnv: "PAIR_TEST_TOKEN"}}}, os.Getenv); err != nil {
		t.Fatalf("err: %v", err)
	}
	if got := h.Get("X-Api-Key"); got != "Bearer real-env" {
		t.Fatalf("value = %q", got)
	}
}

func TestApplyAuth_MissingEnvIsUnavailable(t *testing.T) {
	h := http.Header{}
	err := ApplyAuth(h, LocalAuth{Headers: []HeaderSpec{{Name: "Authorization", ValueEnv: "ABSENT_VAR"}}}, fakeEnv(nil))
	if err == nil {
		t.Fatal("missing env must error")
	}
	if !errors.Is(err, ErrAuthValueUnavailable) {
		t.Fatalf("error should wrap ErrAuthValueUnavailable, got %v", err)
	}
	if h.Get("Authorization") != "" {
		t.Fatal("no header should be set when env is missing")
	}
}

func TestApplyAuth_EmptyEnvIsUnavailable(t *testing.T) {
	h := http.Header{}
	err := ApplyAuth(h, LocalAuth{Headers: []HeaderSpec{{Name: "Authorization", ValueEnv: "EMPTY"}}}, fakeEnv(map[string]string{"EMPTY": ""}))
	if !errors.Is(err, ErrAuthValueUnavailable) {
		t.Fatalf("empty env must be unavailable, got %v", err)
	}
}

func TestApplyAuth_ValueEnvExclusivity(t *testing.T) {
	h := http.Header{}
	if err := ApplyAuth(h, LocalAuth{Headers: []HeaderSpec{{Name: "A", Value: "x", ValueEnv: "Y"}}}, fakeEnv(map[string]string{"Y": "z"})); err == nil {
		t.Fatal("setting both value and valueEnv must error")
	}
	if err := ApplyAuth(h, LocalAuth{Headers: []HeaderSpec{{Name: "A"}}}, nil); err == nil {
		t.Fatal("setting neither value nor valueEnv must error")
	}
}

func TestApplyAuth_InvalidHeaderName(t *testing.T) {
	for _, name := range []string{"", "Bad Name", "has:colon", "new\nline", "tab\tname"} {
		h := http.Header{}
		if err := ApplyAuth(h, LocalAuth{Headers: []HeaderSpec{{Name: name, Value: "v"}}}, nil); err == nil {
			t.Fatalf("invalid name %q must error", name)
		}
	}
}

func TestApplyAuth_NewlineInjectionRejectedAndSecretNotLeaked(t *testing.T) {
	secret := "topsecret-INJECT\r\nX-Evil: pwned"
	h := http.Header{}
	err := ApplyAuth(h, LocalAuth{Headers: []HeaderSpec{{Name: "Authorization", Value: secret}}}, nil)
	if err == nil {
		t.Fatal("newline-bearing value must be rejected")
	}
	if strings.Contains(err.Error(), "topsecret") || strings.Contains(err.Error(), "pwned") {
		t.Fatalf("error leaked the secret value: %v", err)
	}
	if h.Get("X-Evil") != "" {
		t.Fatal("header injection succeeded")
	}
}

func TestApplyAuth_SetReplacesClientValue(t *testing.T) {
	// A client-supplied Authorization must be overwritten, not appended.
	h := http.Header{"Authorization": []string{"Bearer CLIENT-TOKEN"}}
	if err := ApplyAuth(h, LocalAuth{Headers: []HeaderSpec{{Name: "Authorization", Value: "Bearer BACKEND"}}}, nil); err != nil {
		t.Fatalf("err: %v", err)
	}
	vals := h.Values("Authorization")
	if len(vals) != 1 || vals[0] != "Bearer BACKEND" {
		t.Fatalf("Authorization = %v, want single [Bearer BACKEND]", vals)
	}
}

func TestApplyAuth_DuplicateSpecsLastWins(t *testing.T) {
	h := http.Header{}
	_ = ApplyAuth(h, LocalAuth{Headers: []HeaderSpec{
		{Name: "X-Key", Value: "first"},
		{Name: "X-Key", Value: "second"},
	}}, nil)
	if got := h.Get("X-Key"); got != "second" {
		t.Fatalf("last spec should win, got %q", got)
	}
}

func TestApplyAuth_BadEnvName(t *testing.T) {
	h := http.Header{}
	if err := ApplyAuth(h, LocalAuth{Headers: []HeaderSpec{{Name: "A", ValueEnv: "1BAD"}}}, fakeEnv(map[string]string{"1BAD": "x"})); err == nil {
		t.Fatal("env name starting with a digit must error")
	}
}

func TestApplyAuth_NilHeader(t *testing.T) {
	if err := ApplyAuth(nil, LocalAuth{Headers: []HeaderSpec{{Name: "A", Value: "v"}}}, nil); err == nil {
		t.Fatal("nil header must error")
	}
}

func TestStripClientAuth(t *testing.T) {
	h := http.Header{
		"Authorization":       []string{"Bearer client"},
		"Proxy-Authorization": []string{"x"},
		"Content-Type":        []string{"application/json"},
	}
	StripClientAuth(h)
	if h.Get("Authorization") != "" || h.Get("Proxy-Authorization") != "" {
		t.Fatal("client auth headers must be stripped")
	}
	if h.Get("Content-Type") == "" {
		t.Fatal("non-auth headers must be preserved")
	}
	StripClientAuth(nil) // must not panic
}

func TestLocalAuthEmpty(t *testing.T) {
	if !(LocalAuth{}).Empty() {
		t.Fatal("zero LocalAuth is empty")
	}
	if (LocalAuth{Headers: []HeaderSpec{{Name: "A", Value: "v"}}}).Empty() {
		t.Fatal("non-empty LocalAuth reported empty")
	}
}

func TestLocalAuthValidate(t *testing.T) {
	cases := []struct {
		name    string
		a       LocalAuth
		wantErr bool
	}{
		{"empty ok", LocalAuth{}, false},
		{"env ok", LocalAuth{Headers: []HeaderSpec{{Name: "Authorization", ValueEnv: "TOK"}}}, false},
		{"literal ok", LocalAuth{Headers: []HeaderSpec{{Name: "X-Key", Value: "Bearer abc"}}}, false},
		{"bad name", LocalAuth{Headers: []HeaderSpec{{Name: "bad name", Value: "v"}}}, true},
		{"both set", LocalAuth{Headers: []HeaderSpec{{Name: "A", Value: "v", ValueEnv: "E"}}}, true},
		{"neither set", LocalAuth{Headers: []HeaderSpec{{Name: "A"}}}, true},
		{"bad env name", LocalAuth{Headers: []HeaderSpec{{Name: "A", ValueEnv: "1BAD"}}}, true},
		{"newline literal", LocalAuth{Headers: []HeaderSpec{{Name: "A", Value: "x\r\ny"}}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.a.Validate(); (err != nil) != c.wantErr {
				t.Fatalf("Validate() err=%v wantErr=%v", err, c.wantErr)
			}
		})
	}
	// Validate must not leak a literal secret in its error.
	err := (LocalAuth{Headers: []HeaderSpec{{Name: "A", Value: "sekret\nINJECT"}}}).Validate()
	if err == nil || strings.Contains(err.Error(), "sekret") {
		t.Fatalf("Validate leaked secret or missed injection: %v", err)
	}
}

func TestHeaderValidators(t *testing.T) {
	if !validHeaderName("X-Custom_Header.1") {
		t.Fatal("valid token name rejected")
	}
	if validHeaderName("no spaces") || validHeaderName("colon:") || validHeaderName("") {
		t.Fatal("invalid names accepted")
	}
	if !validHeaderValue("Bearer abc.def-123") || !validHeaderValue("with\ttab") {
		t.Fatal("valid values rejected")
	}
	if validHeaderValue("") || validHeaderValue("has\nnewline") || validHeaderValue("has\rcr") || validHeaderValue("bell\x07") {
		t.Fatal("invalid values accepted")
	}
	if !validEnvName("MY_VAR_1") || validEnvName("1BAD") || validEnvName("") || validEnvName("has-dash") {
		t.Fatal("env name validation wrong")
	}
}
