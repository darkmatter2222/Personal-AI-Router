// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import "encoding/json"

// MatchModel resolves which model on an endpoint a requested name refers to.
// The requested name may be a model's physical (upstream) name or one of its
// explicitly declared logical aliases. Physical names take precedence: a
// request for a name that is some model's physical name always resolves to that
// model, even if the same string also appears as another model's alias.
//
// This is the ownership primitive that makes aliases participate in eligibility
// BEFORE any candidate filtering: a client can request the stable logical name
// (e.g. "local-coding") and an endpoint whose physical model is "qwen-fast"
// still matches, with the physical name available for the outbound rewrite.
// Equivalence is always explicit exact-string matching; it is never inferred
// from similar names.
func MatchModel(e Endpoint, requested string) (ModelRouting, bool) {
	if requested == "" {
		return ModelRouting{}, false
	}
	// Pass 1: exact physical match (precedence).
	for _, m := range e.Models {
		if m.Physical == requested {
			return m, true
		}
	}
	// Pass 2: exact alias match.
	for _, m := range e.Models {
		for _, a := range m.Aliases {
			if a == requested {
				return m, true
			}
		}
	}
	return ModelRouting{}, false
}

// Owns reports whether the endpoint serves the requested model (by physical
// name or declared alias).
func Owns(e Endpoint, requested string) bool {
	_, ok := MatchModel(e, requested)
	return ok
}

// PhysicalFor returns the physical model name the requested name maps to on the
// endpoint, and whether the endpoint owns it. When the request already used the
// physical name the result equals the input; when it used an alias the result
// is the alias's physical target.
func PhysicalFor(e Endpoint, requested string) (string, bool) {
	m, ok := MatchModel(e, requested)
	if !ok {
		return "", false
	}
	return m.Physical, true
}

// RewriteModel returns a copy of a JSON request body with its top-level "model"
// field set to physical, leaving every other field byte-for-byte unchanged. It
// reports whether a rewrite was performed. A body that is not a JSON object, or
// a physical name equal to the existing value, is returned unchanged (changed
// false), so callers can skip re-buffering when nothing moved.
//
// The rewrite is done over map[string]json.RawMessage so no other value is
// re-encoded (numbers, nested objects and arrays keep their exact bytes); only
// the model string is replaced. JSON object key order is not significant, so
// the re-marshalled body is semantically identical apart from the model field.
func RewriteModel(body []byte, physical string) (out []byte, changed bool) {
	if physical == "" || len(body) == 0 {
		return body, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return body, false
	}
	// Only rewrite when a model field exists; adding one to a body that never
	// had it would be a surprising mutation.
	cur, ok := obj["model"]
	if !ok {
		return body, false
	}
	var curStr string
	if json.Unmarshal(cur, &curStr) == nil && curStr == physical {
		return body, false // already the physical name
	}
	enc, err := json.Marshal(physical)
	if err != nil {
		return body, false
	}
	obj["model"] = enc
	rewritten, err := json.Marshal(obj)
	if err != nil {
		return body, false
	}
	return rewritten, true
}
