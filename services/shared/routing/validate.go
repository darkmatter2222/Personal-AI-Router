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
	seen := make(map[string]struct{}, len(e.Models))
	for i, m := range e.Models {
		if !validModelName(m.Physical) {
			return fmt.Errorf("routing: model[%d] invalid physical name %q", i, m.Physical)
		}
		if _, dup := seen[m.Physical]; dup {
			return fmt.Errorf("routing: model[%d] duplicate physical name %q", i, m.Physical)
		}
		seen[m.Physical] = struct{}{}
		for j, a := range m.Aliases {
			if !validModelName(a) {
				return fmt.Errorf("routing: model[%d] alias[%d] invalid name %q", i, j, a)
			}
		}
	}
	return nil
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
