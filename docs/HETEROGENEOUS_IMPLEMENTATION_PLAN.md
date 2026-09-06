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
The supplied goal prohibits pushes; all work stays local.
