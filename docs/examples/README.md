<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Example heterogeneous engine manifests

These are reference manifests for the capability-aware routing contract. They are
documentation, not bundled engines. Drop a copy in the per-user engine override
directory to adopt a real backend later; nothing here is loaded automatically.

Both examples are backend-neutral. Neither names a GPU or a host, and neither
requires a new entry in any compiled source enumeration — the engine id is an
arbitrary string.

## generic-external-openai-engine.json

A generic OpenAI-compatible backend PAIR **adopts** but does not own:

- `routing.apiFamily: openai` — routed by wire contract, not brand.
- `routing.lifecycle: external` — PAIR observes, health-checks and routes to it,
  but never installs/starts/stops/restarts/reconfigures it. Only the read-only
  `list_models` action (marked `read_only: true`) may run against it; every
  mutating action is refused before execution.
- `routing.models[].aliases: ["local-coding"]` — a client requests the stable
  logical name `local-coding`; PAIR rewrites the outbound body to the physical
  `actual-upstream-model`.
- `capabilities`/`context` gate the request (text+tools here, 262K window).
- `routing.strategy: deterministic-priority`, `priority: 10` — preferred when
  eligible.
- `capacity: 2` — at most two concurrent admitted requests.
- `timeouts` — a tight connect/header profile with a generous first-byte budget.
- `auth.headers[].valueEnv: PAIR_TEST_BACKEND_TOKEN` — the backend bearer token
  is resolved from the environment on the owning node only. It is applied when
  forwarding to the local backend and NEVER appears in routing metadata,
  discovery, the scheduler, or any peer payload. Use a real environment variable
  name; never put a literal secret in a manifest.

The `runtime` block is still present so PAIR can detect and health-check the
process; because `lifecycle` is `external`, PAIR never uses it to start or stop
the engine.

## vlm-specialist-engine.json

A vision-language specialist showing how a heterogeneous cluster routes by
capability without any hardware-specific code:

- `capabilities.vision: true` — image/multimodal requests are eligible here.
- `priority: 30` — lower preference than the fast text engine above, so ordinary
  text requests prefer the text engine and only vision requests are drawn here
  (a text-only model rejects an image request with `VISION_REQUIRED`).
- `firstByteMs: 120000` — a large model may take a while to produce its first
  token; its generous first-byte budget is local to this endpoint and does not
  weaken any other endpoint's timeout.

Together they demonstrate the intended behaviour: normal text routes to the
preferred fast engine, image requests route to the vision specialist, and both
decisions fall out of the declared capability/priority contract rather than any
`if engine == …` branch.
