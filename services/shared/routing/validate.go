// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import "fmt"

// Validate checks an EngineRouting for well-formedness. It is intentionally
// permissive about absence (every field may be omitted — that selects a
// backward-compatible default) but strict about malformed present values: an
// unknown API family / lifecycle / strategy, a negative capacity or timeout, or
// a model with an empty or control-character-bearing physical name or alias is
// rejected. Validation performs no I/O and never mutates its input.
func (e EngineRouting) Validate() error {
	if !e.APIFamily.Known() {
		return fmt.Errorf("routing: unknown apiFamily %q", e.APIFamily)
	}
	if !e.Lifecycle.Known() {
		return fmt.Errorf("routing: unknown lifecycle %q", e.Lifecycle)
	}
	if !e.Strategy.Known() {
		return fmt.Errorf("routing: unknown strategy %q", e.Strategy)
	}
	if e.Capacity < 0 {
		return fmt.Errorf("routing: negative capacity %d", e.Capacity)
	}
	if e.Timeouts.ConnectMS < 0 || e.Timeouts.ResponseHeaderMS < 0 ||
		e.Timeouts.FirstByteMS < 0 || e.Timeouts.ActionMS < 0 {
		return fmt.Errorf("routing: negative timeout")
	}
	// Priority (a *int) may be any value including negative: lower is preferred,
	// so a negative priority is simply the most-preferred and is intentionally
	// allowed. Capacity 0 = unbounded; a negative capacity was rejected above.
	if e.Health.Path != "" && !validHealthPath(e.Health.Path) {
		return fmt.Errorf("routing: invalid health path %q (must start with '/' and contain no control characters)", e.Health.Path)
	}
	// Physical names and aliases share ONE namespace within an endpoint: a
	// requested name must resolve to exactly one model. Any duplication — a
	// repeated physical name, a repeated alias (within or across models), or an
	// alias that collides with any physical name — is ambiguous (manifest-order
	// dependent) and is rejected. Across DIFFERENT endpoints the same alias
	// mapping to different physical models remains valid; that is the whole
	// heterogeneous abstraction and is out of scope for one EngineRouting.
	seen := make(map[string]struct{})
	claim := func(name, kind string, mi int) error {
		if !validModelName(name) {
			return fmt.Errorf("routing: model[%d] invalid %s name %q", mi, kind, name)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("routing: model[%d] name %q is ambiguous (physical/alias namespace must be unique within an endpoint)", mi, name)
		}
		seen[name] = struct{}{}
		return nil
	}
	for i, m := range e.Models {
		if err := claim(m.Physical, "physical", i); err != nil {
			return err
		}
		for _, a := range m.Aliases {
			if err := claim(a, "alias", i); err != nil {
				return err
			}
		}
	}
	return nil
}

// validHealthPath reports whether s is a usable health-probe path: it must begin
// with '/' and contain no control characters.
func validHealthPath(s string) bool {
	if s == "" || s[0] != '/' {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7F {
			return false
		}
	}
	return true
}

// validModelName reports whether s is a usable model name: non-empty, not
// excessively long, and free of control characters and newlines. Model names
// legitimately contain '/', ':', '.', '-' and '_' (e.g. "repo/qwen-fast:latest"),
// so those are allowed; only characters that could corrupt logs, headers or JSON
// framing are rejected. The physical name is placed into the request body via
// json.Marshal (which escapes safely) and is never interpolated into a URL path
// or header, so this check is defence-in-depth rather than the sole guard.
func validModelName(s string) bool {
	if s == "" || len(s) > 512 {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7F {
			return false
		}
	}
	return true
}
