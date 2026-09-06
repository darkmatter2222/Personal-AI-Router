// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"errors"
	"fmt"
	"net/http"
)

// LocalAuth describes how to authenticate to a LOCAL backend. It is resolved
// ONLY on the node that owns the backend and is deliberately never part of
// EngineRouting, discovery, scheduler data, workload data or any peer-facing
// payload: a credential must never leave the node that owns it, and one node's
// credential must never be sent to another. The proxy on the owning node
// applies it when forwarding to its own backend; a peer forwarding inference to
// this node applies ITS own local credential at ITS own local-backend hop.
type LocalAuth struct {
	Headers []HeaderSpec `json:"headers,omitempty"`
}

// HeaderSpec is one header to apply to an upstream request. Exactly one of
// Value (a literal, discouraged for real secrets) or ValueEnv (the name of an
// environment variable holding the value) must be set.
type HeaderSpec struct {
	Name     string `json:"name"`
	Value    string `json:"value,omitempty"`
	ValueEnv string `json:"valueEnv,omitempty"`
}

// Empty reports whether there is nothing to apply.
func (a LocalAuth) Empty() bool { return len(a.Headers) == 0 }

// Validate checks the auth specs for well-formedness WITHOUT resolving any
// env-backed value (resolution is deferred to ApplyAuth at request time). It is
// meant for manifest load, so a malformed auth block fails loudly and early
// rather than at the first request. Errors never contain a credential value.
func (a LocalAuth) Validate() error {
	for i, spec := range a.Headers {
		if !validHeaderName(spec.Name) {
			return fmt.Errorf("routing: auth header[%d] invalid name %q", i, spec.Name)
		}
		if (spec.Value == "") == (spec.ValueEnv == "") {
			return fmt.Errorf("routing: auth header[%d] %q must set exactly one of value/valueEnv", i, spec.Name)
		}
		if spec.ValueEnv != "" && !validEnvName(spec.ValueEnv) {
			return fmt.Errorf("routing: auth header[%d] invalid env name %q", i, spec.ValueEnv)
		}
		if spec.Value != "" && !validHeaderValue(spec.Value) {
			return fmt.Errorf("routing: auth header[%d] %q has an invalid literal value", i, spec.Name)
		}
	}
	return nil
}

// ErrAuthValueUnavailable indicates a header's backing environment variable was
// unset or empty. Callers map it to ReasonAuthUnavailable. The error never
// contains the credential value.
var ErrAuthValueUnavailable = errors.New("routing: auth header value unavailable")

// ApplyAuth applies a LocalAuth's headers to h, resolving env-backed values via
// getenv (pass os.Getenv in production; a fake in tests). It uses Set semantics,
// so a configured header replaces any value already present (including one a
// client tried to supply), and later specs for the same header name override
// earlier ones. It validates header names and values and refuses to set an
// invalid or injected header.
//
// Errors never include the credential value — only the (non-secret) header name
// or environment variable name — so a failure is safe to log. A returned error
// wrapping ErrAuthValueUnavailable means an env-backed value was missing/empty.
func ApplyAuth(h http.Header, a LocalAuth, getenv func(string) string) error {
	if h == nil {
		return errors.New("routing: nil header")
	}
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	for _, spec := range a.Headers {
		if !validHeaderName(spec.Name) {
			return fmt.Errorf("routing: invalid auth header name %q", spec.Name)
		}
		if (spec.Value == "") == (spec.ValueEnv == "") {
			// both empty or both set
			return fmt.Errorf("routing: auth header %q must set exactly one of value/valueEnv", spec.Name)
		}
		var val string
		if spec.ValueEnv != "" {
			if !validEnvName(spec.ValueEnv) {
				return fmt.Errorf("routing: invalid env name %q for header %q", spec.ValueEnv, spec.Name)
			}
			val = getenv(spec.ValueEnv)
			if val == "" {
				return fmt.Errorf("routing: header %q env %q: %w", spec.Name, spec.ValueEnv, ErrAuthValueUnavailable)
			}
		} else {
			val = spec.Value
		}
		if !validHeaderValue(val) {
			// Do NOT include val in the error — it is the secret.
			return fmt.Errorf("routing: auth header %q has an invalid value (control character or newline)", spec.Name)
		}
		h.Set(spec.Name, val)
	}
	return nil
}

// StripClientAuth removes client-supplied authentication headers from an
// upstream request header set. The proxy calls this before ApplyAuth so a client
// can never smuggle its own credential to the backend and so a backend's local
// credential is the only Authorization ever sent.
func StripClientAuth(h http.Header) {
	if h == nil {
		return
	}
	h.Del("Authorization")
	h.Del("Proxy-Authorization")
}

// validHeaderName reports whether s is a valid HTTP field name (RFC 7230 token):
// non-empty and composed only of token characters. This rejects names
// containing spaces, colons, control characters or newlines.
func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isTokenChar(s[i]) {
			return false
		}
	}
	return true
}

// isTokenChar reports whether c is an RFC 7230 tchar.
func isTokenChar(c byte) bool {
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	}
	switch {
	case c >= '0' && c <= '9':
		return true
	case c >= 'a' && c <= 'z':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	}
	return false
}

// validHeaderValue reports whether s is a safe HTTP field value: visible ASCII
// plus space and horizontal tab, and specifically no CR or LF (header/newline
// injection) and no other control characters or DEL. An empty value is invalid
// (an empty credential is never intended).
func validHeaderValue(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\t' {
			continue
		}
		if c < 0x20 || c > 0x7E {
			return false
		}
	}
	return true
}

// validEnvName reports whether s is a plausible environment-variable name: a
// non-empty run of letters, digits and underscores not starting with a digit.
// This guards against a manifest injecting a strange lookup key.
func validEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_':
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
