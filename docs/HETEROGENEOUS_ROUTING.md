<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Heterogeneous capability-aware routing

This document describes the generic, capability-aware inference control plane
added to PAIR: how an operator declares a heterogeneous backend, how a request
is matched to an eligible endpoint, and how the design preserves deterministic
operator intent, security boundaries and backward compatibility.

It is deliberately backend-neutral. Nothing here names a specific GPU, host or
inference runtime; the whole point is that the runtime should be an integration
detail once its contract is declared.

## The routing pipeline

Every request flows through four stages, isolated so the decision is a pure,
exhaustively testable function and only the final stage does I/O:

```
CLASSIFY  ->  DECIDE  ->  RESERVE  ->  FORWARD
```

| Stage | What it does | Where |
| --- | --- | --- |
| CLASSIFY | Summarise what the request needs (model, images, tools, streaming, context) without keeping its content | `routing.Classify` |
| DECIDE | Filter endpoints by eligibility, then order the survivors by the selected policy | `routing.Decide` |
| RESERVE | Take an authoritative admission permit for the chosen endpoint | `routing.Pools.Reserve` |
| FORWARD | Rewrite the model, apply local auth, forward with per-endpoint timeouts, stream back, fail over before commit | `routing.Forward` |

The cardinal rule is **eligibility before scheduling**: an endpoint that cannot
satisfy a request is removed before any ordering, so no idle, less-loaded or
scheduler-preferred endpoint can ever receive a request it cannot serve.

## Declaring an engine

Routing metadata is declared per engine in the engine manifest and propagated,
credential-free, on each node's discovery record (`DirectoryNode.RoutingByEngine`).
The declarative contract (`routing.EngineRouting`) is:

```yaml
engine: custom-runtime          # arbitrary id — no source enum entry required
display_name: Custom Runtime
api_family: openai              # wire contract, not a brand: openai | ollama

lifecycle:
  mode: external                # managed (default) | external (adopt-only)

routing:
  strategy: deterministic-priority   # default | deterministic-priority
  priority: 10                       # lower = preferred; omitted = lowest
  static_capacity: 2                 # max concurrent admissions; 0/omitted = unbounded

timeouts:
  connect_ms: 250
  response_header_ms: 30000
  first_byte_ms: 30000               # distinct from response_header_ms (see below)

health:
  path: /health

models:
  - physical: actual-upstream-model  # the name the backend really serves
    aliases: [local-coding]          # stable logical names a client may request
    capabilities: { text: true, vision: false, tools: true, streaming: true }
    context: { max_tokens: 262144 }
```

`models` is a list because capabilities and context live at the **model**
scope, not the engine scope: a multi-model engine can serve a 32K text model, a
128K vision model and a 262K tool model behind one endpoint, each gated
correctly. A single-model process simply lists one model.

## Capabilities

`text`, `vision`, `tools` and `streaming` are **enforced**:

- A generation request always requires `text`. A `text: false` model is never
  selected for a normal request.
- An image/multimodal request requires `vision`.
- A tool-calling request requires `tools`. A preferred, idle endpoint with
  `tools: false` loses to a lower-priority endpoint that can call tools.
- A streaming request requires `streaming`.

`reasoning`, `embedding` and `audio` are accepted and validated as
**metadata-only**: they are carried in the contract but not yet gated on. They
are documented as deferred rather than presented as active behaviour.

## Context eligibility

The classifier produces a conservative (deliberately high) estimate of the
request's required context — input token estimate plus requested output tokens.
An endpoint whose model declares a smaller `max_tokens` is rejected with
`CONTEXT_TOO_SMALL`. A model that declares `max_tokens: 0` (undeclared) disables
the gate rather than being treated as a zero-size window. Estimation is
intentionally not tokenizer-exact; over-estimating is the safe direction because
it rejects a borderline endpoint rather than overflowing it.

## Model aliases

A client requests a stable logical name (for example `local-coding`); different
endpoints map it to different physical models (`qwen-fast`, `qwen-q4`,
`flash-next`). Alias equivalence is always **explicit** — declared in the
`aliases` list — never inferred from similar names.

Aliases participate in ownership **before** candidate filtering: matching
resolves a requested name to a model by physical name or declared alias in one
step, and the resolved physical name travels to FORWARD, which rewrites the
outbound request body's `model` field. A request for a logical alias therefore
never "disappears" for lack of a physical inventory match.

## Routing strategies

Exactly one strategy governs a decision; strategies never run in sequence and
never reorder one another:

- **default** — after eligibility filtering, preserve PAIR's existing scheduler
  ordering. This is the zero value and the backward-compatible behaviour.
- **deterministic-priority** — after eligibility filtering, order strictly by
  the configured `priority` (lower preferred), then by stable endpoint id.

When a set of eligible endpoints mixes strategies, explicit operator intent
dominates deterministically: if any endpoint requests deterministic-priority,
the whole decision uses it. This is a single documented rule, not two policies
run back to back.

Priority defaults matter: an **omitted** priority resolves to the lowest
preference, so an unconfigured endpoint never accidentally becomes the
most-preferred one. An explicit `0` is distinct and is the highest preference.

## Capacity and admission

`static_capacity` is an operator policy for the maximum number of concurrently
admitted requests, not a claim about backend concurrency. Admission is
concurrency-safe and authoritative:

- A request is forwarded only if its reservation succeeded. A failed reservation
  is final: that endpoint is skipped with `CAPACITY_FULL` and the request tries
  the next eligible endpoint.
- Capacity is released exactly once on every terminal path — normal completion,
  backend error, client/context cancellation, timeout, pre-stream failover,
  stream error — with no leak, double-release or negative counter.
- `0` (or omitted) capacity means unbounded (the legacy default): no admission
  gate is applied, though in-flight count is still tracked.
- Capacity is reconfigurable: expansion, contraction, contraction below the
  active count, endpoint removal and recreation are all handled without a cached
  pool remaining stale. In-flight requests to a removed endpoint keep their own
  reservation until they finish; a recreated endpoint starts fresh.

## Timeouts

Timeouts are per-endpoint and each is resolved against a caller default
independently, so a slow large-model endpoint's generous profile never weakens a
fast endpoint's tight one. A `0` field falls back to the default.

`response_header_ms` and `first_byte_ms` are deliberately distinct. Receiving a
`200` with headers is not the same as receiving the first inference token: an
OpenAI-style backend can return headers immediately and then spend seconds
producing the first token. A cold large model therefore wants a generous
`first_byte_ms` even with a tight `response_header_ms`. A first-byte timeout that
fires before any byte reaches the client is a failover trigger; once the first
byte is written the response is committed and never retried.

## Authentication and secret boundaries

A backend may require a header such as `Authorization: Bearer …`. This is a
**node-local** concern and is modelled by `routing.LocalAuth`, which is
deliberately **not** part of the routing metadata, discovery, scheduler data or
any peer-facing payload. Guarantees:

- A credential is resolved (typically from an environment variable) only on the
  node that owns the backend, and applied only when forwarding to that local
  backend. One node's credential is never sent to another; a peer forwarding
  inference to this node applies its own local credential at its own hop.
- Header names and values are validated (RFC 7230 token names; no CR/LF or other
  control characters in values) to prevent header/newline injection. A
  client-supplied `Authorization` is stripped before the backend credential is
  applied, so a client can never smuggle a credential to the backend.
- Errors never contain the credential value; only the (non-secret) header or
  environment-variable name.

## Explainable rejections

Every exclusion carries a stable, machine-readable reason safe to log and assert
on, and a routing explanation contains only ids, engine names, model names and
reason codes — never prompts, generated content, images, tool arguments or
secrets. The codes are:

```
MODEL_NOT_AVAILABLE   API_FAMILY_INCOMPATIBLE   TEXT_REQUIRED
VISION_REQUIRED       TOOLS_REQUIRED            STREAMING_REQUIRED
CONTEXT_TOO_SMALL     ENDPOINT_DISABLED         ENDPOINT_UNHEALTHY
ENDPOINT_DRAINING     CAPACITY_FULL             AUTH_UNAVAILABLE
```

## Endpoint state

An endpoint is `enabled`, `disabled`, `draining`, `healthy` or `unhealthy`.
Enabled/disabled uses a `Disabled` boolean whose zero value is **enabled**, so
an omitted field can never silently turn an endpoint off — the safe default is
the one the zero value already gives. Health is runtime state maintained by the
control plane, not declared config.

## Backward compatibility and legacy defaults

New gating engages only for endpoints an operator has actually described:

- An engine that advertises **no** routing metadata keeps PAIR's existing
  behaviour: existing model-inventory ownership, the existing scheduler, existing
  transports and timeouts. It is never treated as "incapable" and disabled.
- Within routing metadata, every omitted field resolves to a permissive/legacy
  default: enabled, unbounded capacity, default timeouts, default strategy,
  lowest explicit priority, and an undeclared context window disables the context
  gate.

This lets the heterogeneous path be strictly opt-in and additive: legacy Ollama
and LM Studio nodes continue to work unchanged, while an operator can describe a
new backend and immediately get capability-aware routing to it.

## Generic engine identity

An engine id is an arbitrary string end to end. The control-plane data model
keys models, loaded state and routing metadata by that string
(`map[string]…`), the scheduler emits per-engine priority for every discovered
engine, and the desktop separates a closed `BuiltInEngineType` (branding,
dedicated install UX) from an open `EngineId` (identity, inventory, workloads).
A future runtime that does not exist today survives manifest → registry →
inventory → routing metadata → discovery → scheduler → desktop state without any
new entry in a compiled source enumeration.
