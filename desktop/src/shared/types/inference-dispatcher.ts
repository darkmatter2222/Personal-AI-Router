// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

/**
 * Types describing the standalone `inference-dispatcher` Go binary's surface.
 *
 * The binary is a third-party-style HTTP client: one backend, one model, N
 * requests per invocation. PAIR drives it only from the Inference Demo
 * (`@/electron/inference-demo`), which spawns it once per scheduled request and
 * reads its `--list-models` inventory.
 */

// Open backend set: the two built-in backends are the known values, but a
// heterogeneous fleet may dispatch to any backend name (vLLM, torch, a custom
// runtime). `(string & {})` keeps autocomplete for the known values while
// accepting arbitrary backend strings.
export type DispatcherBackend = 'ollama' | 'lmstudio' | (string & {})

/** One entry from the binary's `--list-models` JSON inventory. */
export interface DispatcherModel {
    name: string
    capabilities?: string[]
    type?: string
}
