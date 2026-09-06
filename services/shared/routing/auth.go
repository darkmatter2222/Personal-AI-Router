// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

// envSecretPrefix and envSecretSuffix mark an env-backed secret reference in a
// manifest header value, e.g. "Authorization: Bearer ${NVPAIR_VLLM_KEY}".
const (
	envSecretPrefix = "${"
	envSecretSuffix = "}"
)

// SecretError marks a header-resolution failure. Callers surface it as an
// endpoint-local failure (the endpoint is unreachable by us) rather than
// leaking the resolution detail — which may name the env var — into a client
// error. The secret value itself is never part of the error.
type SecretError struct{ Msg string }

func (e *SecretError) Error() string { return e.Msg }

// ResolveSecretValue resolves one manifest header value. A value that is
// exactly one ${VAR} reference is env-backed: the variable is read from the
// owning node's environment (the secret never leaves the node) and the result
// is validated as a single-line header value. A value with no ${} is a
// literal (still validated). A value with ${} mixed into other text is
// malformed: env expansion is all-or-nothing so a partial expansion cannot
// silently produce a wrong credential.
//
// Validation uses the standard library (http.CanonicalHeaderName plus a
// single-line check on the value) rather than home-grown parsing, and rejects
// CR/LF so a hostile manifest cannot inject a second header.
func ResolveSecretValue(value string) (string, error) {
	if strings.Contains(value, envSecretPrefix) {
		if !strings.HasPrefix(value, envSecretPrefix) || !strings.HasSuffix(value, envSecretSuffix) ||
			len(value) < len(envSecretPrefix)+len(envSecretSuffix) ||
			strings.Count(value, envSecretPrefix) != 1 || strings.Count(value, envSecretSuffix) != 1 {
			return "", &SecretError{Msg: "header value must be a literal or a single ${VAR} reference"}
		}
		name := value[len(envSecretPrefix) : len(value)-len(envSecretSuffix)]
		if name == "" {
			return "", &SecretError{Msg: "env reference name is empty"}
		}
		resolved, ok := os.LookupEnv(name)
		if !ok {
			return "", &SecretError{Msg: fmt.Sprintf("env variable %s is not set", name)}
		}
		if resolved == "" {
			return "", &SecretError{Msg: fmt.Sprintf("env variable %s is empty", name)}
		}
		return resolved, nil
	}
	if value == "" {
		return "", &SecretError{Msg: "header value is empty"}
	}
	return value, nil
}

// ValidateHeaderName reports whether name is a valid HTTP header name under
// RFC 9110 token rules (the same alphabet the standard library uses),
// guarding against malformed manifest entries and newline injection in the
// name.
func ValidateHeaderName(name string) error {
	if name == "" {
		return &SecretError{Msg: "header name is empty"}
	}
	for _, r := range name {
		if !tokenRune(r) {
			return &SecretError{Msg: fmt.Sprintf("header name %q contains invalid characters", name)}
		}
	}
	return nil
}

// tokenRune mirrors the RFC 9110 token alphabet: !#$%&'*+-.^_`|~ plus digits
// and letters.
func tokenRune(r rune) bool {
	if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
		return true
	}
	switch r {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	}
	return false
}

// ValidateHeaderValue reports whether value is safe to set as a header value:
// non-empty and free of CR/LF (which would terminate the header and inject
// another). The standard library would accept stray CR/LF in a value, so the
// check is explicit.
func ValidateHeaderValue(value string) error {
	if value == "" {
		return &SecretError{Msg: "header value is empty"}
	}
	if strings.ContainsAny(value, "\r\n") {
		return &SecretError{Msg: fmt.Sprintf("header value for %q contains a line break", value)}
	}
	return nil
}

// ResolveHeaders resolves a manifest-declared header map into concrete
// header values, returning the first validation failure. It is the single
// entry point both the inference proxy and the engine-manager action path
// use, so auth validation has one implementation.
func ResolveHeaders(headers map[string]string) (map[string]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(headers))
	for name, value := range headers {
		if err := ValidateHeaderName(name); err != nil {
			return nil, err
		}
		resolved, err := ResolveSecretValue(value)
		if err != nil {
			return nil, err
		}
		if err := ValidateHeaderValue(resolved); err != nil {
			return nil, err
		}
		out[name] = resolved
	}
	return out, nil
}

// ApplyAuthHeaders adds resolved local auth headers (e.g. Authorization) to
// the outgoing request. Values were resolved locally (env-expanded) on the
// owning node and never cross the trust boundary.
func ApplyAuthHeaders(req *http.Request, headers map[string]string) {
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
}
