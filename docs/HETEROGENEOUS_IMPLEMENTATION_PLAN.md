<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Heterogeneous control-plane implementation plan

Baseline: local main, commit 13b6811. No historical experimental branch is used.
This is an offline implementation, not a deployment or end-to-end exercise.

## Current architecture and coupling audit

- Engine-manager's registry already keys manifests by arbitrary strings. Runtime
  process/command modes, imperative actions, desired-state reconciliation and
  shutdown currently assume ownership. External adoption needs guards at the
  execution boundaries, not just JSON parsing.
- Inventory originates in engine-manager ModelsResult. Scanner fetches it via
  loopback or pinned peer mTLS and maintains DirectoryNode.ModelsByEngine and
  LoadedByEngine. Broker projects those maps into AvailableNode. Routing metadata
  must follow the same authenticated path, with replacement/removal semantics.
- Ollama and LM Studio proxies own inference, exact/native model ownership,
  ordered failover, per-peer transports and workload reporting. The broker never
  transports inference bodies. Current local reservations are scheduler estimates,
  not hard capacity permits. Terminal peer ingress bypasses routing; owner-side
  admission must also be enforced there to bound aggregate peer concurrency.
- Scheduler ranks nodes by shared workload/GPU pressure but emits only the two
  built-in engine names. Discovery must supply engines even with zero workloads.
- Desktop engine constants, bridge normalization and engine-specific maps narrow
  identity to two built-ins. Branding and management affordances must remain
  closed while inventory/workloads/errors become open.
- mTLS ownership stays in clustertrust; generic endpoints must not allow callers
  to select unauthenticated remote engine URLs or forward local credentials to peers.

Repository-wide engine-name search classification:

| Category | Examples | Treatment |
| --- | --- | --- |
| Protocol-specific | Ollama native routes/tag normalization; OpenAI envelopes | Preserve |
| Lifecycle-specific | vendor CLI/install/remove and manifest commands | Preserve only for managed integrations |
| Built-in branding | icons, catalogs, dedicated install UI | Preserve separately from engine identity |
| Architectural coupling | proxyForEngine, scheduler engine list, scanner/proxy service keys | Requires generic contract transport |
| Closed enumeration | desktop EngineType and bridge filters | Separate identity from built-in presentation |

## Intended sequence

1. Isolate secret-free model/endpoint contracts, conservative request classification,
   alias ownership/rewrite, pure eligibility/policy and concurrent admission.
2. Add bounded fake-transport execution with authoritative reservation, first-body-byte
   versus response-header deadlines, local-only auth and no retry after commitment.
3. Extend existing manifests and enforce external lifecycle in all mutation paths.
4. Propagate validated metadata through inventory, scanner, broker and scheduler;
   integrate both proxy facades and terminal ingress without a second scheduler.
5. Open desktop domain boundaries while keeping built-in controls gated.
6. Run deliberately allowlisted offline tests, review secrets/trust/concurrency,
   record direct-function evidence and update service versions for changed binaries.

Each slice must remain explicitly labelled as foundation versus wired production
behavior. A new helper package alone does not establish generic PAIR inference.

## Offline validation constraints discovered

Go/gofmt were not found on PATH or in standard Go installation locations.
Desktop node_modules is absent. Git author name/email are unset. Do not install
or fetch dependencies, fabricate commit identity, or treat unavailable tests as
passing. Existing service TestMain functions and listener/subprocess tests make
broad go test ./... unsafe; the offline script must use an explicit allowlist.

## Cross-engine hardening pass

This pass turned the routing foundation into genuinely heterogeneous,
cross-engine routing and hardened the request path. Delivered offline and
verified by adversarial model review (no Go toolchain is present to execute):

- **Cross-engine routing.** `routeadapter` now considers candidates across ALL
  engine ids a node declares (optional `EngineFilter`), not one engine per
  router. One alias spans different runtimes; each outbound request carries the
  chosen endpoint's own physical model name.
- **Node vs endpoint identity.** `routing.Endpoint`/`Placement` carry both
  `NodeID` (scheduler rank) and `EndpointID` (admission/tie-break/target),
  built with a collision-free `routing.EndpointKey`. Multiple engines on one node
  admit and fail over independently while sharing one rank.
- **Strategy after eligibility.** `routing.DecideResolved` resolves the strategy
  from the ELIGIBLE set only, so an ineligible endpoint cannot dictate ordering.
- **Safe request bodies.** Oversize → 413 (no truncation, no upstream), read
  error → 400, malformed JSON → 400 (not a routing 503), legacy fallback
  preserves the body byte-for-byte.
- **Mixed clusters.** Enhanced-empty-`Models` and legacy nodes synthesise
  conservative inventory candidates (text/streaming only; no invented
  vision/tools).
- **External/adopt runtime mode** with validation and a guard that treats it as
  adopt-only even without routing metadata; action-contradiction validation
  (`read_only`+`restart_after`/`remove_path`, negative timeout, `slow_load` on a
  non-HTTP action); external engines may declare only read-only actions.
- **Configurable context reserves** (`routing.ReservePolicy`).
- **Accurate reason codes** (a 408/429 is `UPSTREAM_RETRYABLE_STATUS`, not
  `UPSTREAM_5XX`).
- **Readiness script** separates mandatory failures from optional skips (an
  optional skip no longer fails readiness), adds a routeadapter coverage gate and
  runs a pure, non-spawning engine-manager validation/guard subset in the gate.

Not yet done (gated on a Go toolchain, which is absent here): executing the
suites; wiring the shared adapter into the live `ollama-proxy`/`lmstudio-proxy`
handlers (designed, deferred so the live inference path is compiled and verified,
not blind-edited); owner-side admission at cluster ingress; widening desktop
`EngineId` for generic identities. The verdict remains NOT READY FOR REAL-BACKEND
INTEGRATION TESTING until the suites run green twice on a toolchain box and the
proxy wiring lands. Pushes to the user's fork are authorised this session.
