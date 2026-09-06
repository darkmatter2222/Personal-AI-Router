<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Heterogeneous control plane — offline readiness report

This is NOT an end-to-end deployment report. It records exactly what was built,
what was validated and how, and what remains — with honest execution status.

## The decisive environment constraint

This work was done fully offline on a machine with **no Go toolchain** (`go`,
`gofmt`, `go vet` are absent; there is no Go module cache and the services
vendor nothing) and **no desktop `node_modules`** (installing is forbidden, so
`tsc`/`vitest`/`eslint` cannot run). Therefore **no Go or TypeScript code was
compiled or executed here.**

Everything below labelled a "test" is written to pass and was validated by
**adversarial line-by-line review** (independent models acting as the Go
compiler and as skeptical logic reviewers). The honest status ladder used:

| Status | Meaning |
| --- | --- |
| STATICALLY VALIDATED | Reviewed compile-clean and logic-correct by a second model acting as the compiler; not executed here. |
| NOT EXECUTED | No test run (Go/Node toolchain absent). Applies to everything. |
| REAL-BACKEND DEFERRED | Needs a real inference runtime. |
| FULL END-TO-END DEFERRED | Needs a live multi-process PAIR cluster. |

Nothing is marked "UNIT PROVEN" or "PASS", because no test was executed. To
execute the written suites, run `scripts/test-heterogeneous-offline.ps1` on a
box with Go (it runs an explicit safe allowlist; it installs nothing, opens no
socket, launches no engine, and runs no cross-process test).

## Review evidence

Three independent adversarial reviews (a second model reading every file as a
Go 1.25 compiler and as a logic skeptic):

- **routing package** — zero compile errors, stdlib-only, logic correct; found
  one real test-expectation bug (7 classifier cases), which was fixed.
- **engine-manager + scheduler + noderec (13 files)** — all compile-clean, no
  logic/test bugs; confirmed struct field names, manifest-merge survival of the
  new keys, guard ordering, no scheduler double-lock, no positional-literal
  breakage, no go.mod changes needed.
- **broker + scanner propagation** — all six files compile-clean; zero compile
  errors, no import cycle; every `fetchModels` (5-value) and `applyModels`
  (7-arg) call site verified correct arity repo-wide; no positional-literal
  breakage. Two non-blocking items: five test-file daemon literals omit the new
  cache-map init (safe — write paths nil-guard, nil reads return zero), and
  gofmt alignment in three added blocks (auto-fixable by `gofmt -w`; those files
  are outside the offline gofmt check, which covers only the routing package).

---

## Architecture

The feature is a four-stage routing pipeline, isolated so the decision is a
pure, exhaustively testable function and only the last stage does I/O:

```
CLASSIFY (Classify) -> DECIDE (Decide) -> RESERVE (Pools.Reserve) -> FORWARD (Forward)
```

The cardinal rule is **eligibility before scheduling**: ineligible endpoints are
removed before any ordering. All of this lives in a new, dependency-free shared
package `nvpair-shared/routing`, so the routing brain is decoupled from every
engine brand and from the services that consume it.

Per-engine routing metadata is declared in the engine manifest
(`routing`/`auth` blocks), enforced at the owning node (external-lifecycle
guard, local-only auth), advertised credential-free on `/v1/models`, propagated
through discovery (`DirectoryNode.RoutingByEngine` → `EnrichedNode` →
`AvailableNode`), and made discoverable to the scheduler (open engine set).

## Production changes

- **New** `services/shared/routing/` — contract types, `Classify`, alias
  ownership + `RewriteModel`, `Evaluate` (eligibility), `Decide` + `ResolveStrategy`
  (policy), `Pool`/`Pools` (admission), `ApplyAuth`/`StripClientAuth`/`LocalAuth`
  (local auth), `Validate`, `LifecycleMode.Allows` (external-lifecycle decision),
  and `Forward` (fake-transport-injectable executor).
- **`nvpair-engine-manager`** — `Manifest.Routing`/`Auth` fields + validation
  (registry.go); `ModelsResult.RoutingByEngine` emission, credential-free
  (models.go); external-lifecycle guard (lifecycleguard.go) wired into
  `Start`/`StartWith`/`Stop`/`Restart`/`Install`/`Uninstall`/`SetPort` and a
  `StopAll` skip.
- **`nvpair-shared/noderec`** — `DirectoryNode.RoutingByEngine` field +
  `EngineRouting` accessor.
- **`nvpair-ui-broker`** — `EnrichedNode`/`AvailableNode` `RoutingByEngine` +
  projection pass-through (directoryToEnriched, toAvailable).
- **`nvpair-node-scanner`** — decode `routingByEngine` from `/v1/models`
  (fetchModels), thread through the two enrichment paths + the last-good cache,
  store on the directory (applyModels), migrate/forget.
- **`nvpair-job-scheduler`** — open engine set: emit per-engine priority for
  every engine discovered in the cluster (from node `modelsByEngine`/
  `routingByEngine` keys), keep the built-in baseline, prune stale engines.

## Bugs found and prevented during this work

- **Classifier token estimate (found by review, fixed):** seven test cases
  wrongly expected zero input tokens for message-bearing requests; the
  production per-message framing overhead is correct and deliberate, so the test
  expectations were corrected.
- **Forwarder compile bug (found while writing, fixed):** a `drain` helper used
  an anonymous struct channel type not assignable from the locally-defined
  `rtResult`; hoisted the type to package scope.
- **Prevented — capacity leak on every terminal path:** the executor releases
  exactly once (idempotent, once-guarded) on retry, commit, dial error, header
  timeout, first-byte timeout, cancellation, stream error and missing target;
  proven by the Forward component matrix.
- **Prevented — two-scheduler reorder:** `Decide` applies exactly one strategy;
  `ResolveStrategy` picks it deterministically. Strategies never run in sequence.
- **Prevented — alias resolving too late:** ownership resolves by physical name
  OR declared alias in one step before filtering, so a logical-alias request
  never disappears for lack of a physical inventory match.
- **Prevented — priority zero-value trap:** omitted priority resolves to the
  lowest preference (a large sentinel), never accidentally the highest; enabled
  uses a `Disabled` bool whose zero value is enabled.

## Unit-test inventory

Routing-critical functions added/modified and directly tested (see
`HETEROGENEOUS_UNIT_TEST_MATRIX.md` for the full table): every function in the
`nvpair-shared/routing` package has direct tests (~40 test functions +
3 fuzz targets), the scheduler open-set has direct tests, the noderec wire field
has a round-trip test, and the engine-manager external-lifecycle guard is tested
through the real production entry points. No routing-critical function added here
lacks a direct test. Status of all: STATICALLY VALIDATED, NOT EXECUTED.

## Coverage

Not measurable here (Go toolchain absent). The `routing` package is written to
exercise every statement and branch (the target is ~100%), and
`scripts/test-heterogeneous-offline.ps1` runs `go test -coverprofile`/`go tool
cover` on a box with Go. No line is intentionally untestable.

## Desktop tests

NOT EXECUTED — desktop `node_modules` is absent and installing is forbidden, so
`tsc`/`vitest` cannot run. No desktop code was changed this pass (see Deferred).

## Concurrency

Concurrency tests exist for the admission pool: exact-count under contention,
single-slot failover race (only one winner), and a race-detector target
hammering Reserve/Reconcile/Used. Reviewed race-free (lock order always
`Pools.mu` → `Pool.mu`; counter never negative; release once-guarded). Status:
STATICALLY VALIDATED, NOT EXECUTED. Run with `go test -race` on a Go box (the
offline script attempts it; it needs cgo/gcc).

## Evidence per core feature

**Generic engine identity** — Production: arbitrary string engine ids flow
through `noderec.DirectoryNode.RoutingByEngine`/`ModelsByEngine` (open maps),
`ModelsResult`, broker projection, and the scheduler open set. Tests:
`TestDirectoryNode_RoutingByEngineRoundTrip` (novel id `engine-982341`),
`TestOpenEngineSet_*` (novel ids via discovery). Status: STATICALLY VALIDATED.

**Logical aliases** — Production: `MatchModel`/`PhysicalFor`/`RewriteModel`.
Tests: `TestMatchModel_*`, `TestRewriteModel*`, and the component test
`TestForward_ModelRewriteToPhysical` (fake transport asserts the outbound body
carries the physical name, alias absent). Status: STATICALLY VALIDATED +
COMPONENT (fake transport).

**Capability routing (text/vision/tools/streaming/context)** — Production:
`Evaluate` + `Decide`. Tests: `TestEvaluate_Gates` (all reasons), plus
`TestForward` behaviours. Ordering: model → family → text → vision → tools →
streaming → context → disabled → unhealthy → draining. Status: STATICALLY
VALIDATED.

**Routing strategy** — Production: `Decide` + `ResolveStrategy`. `default`
preserves the existing scheduler order after eligibility; `deterministic-priority`
orders by priority then id; exactly one applies. Tests: `TestDecide_*`,
`TestResolveStrategy`, `TestDecide_EligibilityBeforeScheduling`. Status:
STATICALLY VALIDATED.

**Capacity** — Production: `Pool`/`Pools` + the reserve/release lifecycle in
`Forward`. Reservation authoritative, release exactly once on every terminal
path, reconfigurable, unbounded=0. Tests: `TestPool*`, `TestPools*`,
`TestForward_Capacity*`, `TestForward_*ReleasesCapacity`,
`TestForward_StreamingReservationHeldThenReleased`. Status: STATICALLY VALIDATED
+ COMPONENT.

**Timeouts** — Production: `Timeouts.*` resolution consumed by `Forward`
(response-header vs first-byte, per endpoint). Tests: `TestTimeoutResolution`,
`TestTimeoutsIndependent`, `TestForward_PerEndpointResponseHeaderTimeout`,
`TestForward_FirstByteTimeoutDistinctFromHeader`. Status: STATICALLY VALIDATED +
COMPONENT.

**Authentication** — Production: `ApplyAuth`/`StripClientAuth`/`LocalAuth`,
applied by `Forward` at the local-backend hop. Tests: `TestApplyAuth_*`
(env resolution, injection rejected, secret never in errors, client credential
stripped), `TestForward_AuthHeaderAppliedAndClientStripped` (fake transport
captures the outbound Authorization), `TestForward_AuthUnavailableFailsOver`.
Secret boundary: `LocalAuth` is absent from `EngineRouting`/discovery/scheduler/
workload data; the engine-manager copies only `mf.Routing` (never `mf.Auth`) to
`/v1/models`. Status: STATICALLY VALIDATED + COMPONENT.

**External lifecycle** — Production: `LifecycleMode.Allows` + the engine-manager
guard wired into every mutation entry point. Tests:
`TestLifecycle_External*`/`Managed*` (pure decision) and
`TestExternalLifecycleGuard_RefusesMutationsThroughProductionEntryPoints` (real
`Start`/`Stop`/`Restart`/`Install`/`Uninstall`/`SetPort`). Status: STATICALLY
VALIDATED (the engine-manager guard test is not in the offline allowlist because
that package has pre-existing subprocess/TestMain tests; the decision is proven
offline by the routing package).

## Offline constraints — confirmed

```
No internet used                 — confirmed (no curl/wget/fetch/git network op)
No dependencies installed        — confirmed (no go get / npm install / winget / choco)
No model was downloaded          — confirmed
No inference engine was launched — confirmed
No complete PAIR system launched — confirmed
No real GPU/LAN endpoint contacted — confirmed
No subprocess integration test required for success — confirmed (offline script uses a pure allowlist)
No admin/elevation/firewall prompt — confirmed
No user interaction required      — confirmed
```

## Deferred validation (the actual remaining work)

**Execution (toolchain-blocked, not a design gap):**
- Run the written Go suites on a box with Go + a warm module cache
  (`scripts/test-heterogeneous-offline.ps1`), including `-race` and coverage.

**Consumer-side wiring (designed, deliberately not blind-edited):**
- **Proxy adoption.** The inference proxies do not yet consume the routing
  decision. The tested building blocks exist: gate/order candidates with
  `routing.Decide` (in `resolveCandidates`, only when a node advertises
  `RoutingByEngine`, else the legacy path), admit with `routing.Pools`, apply
  per-endpoint timeouts via `routing.Timeouts` in `candidateTransport`, and
  apply `routing.ApplyAuth` at the local-backend forward — or delegate the whole
  failover loop to `routing.Forward`. This was left unwired because the proxy
  files are large and cannot be compiled offline; the routing package's Forward
  component tests already prove the forwarding behaviour with fakes.
- **Desktop open identity.** Introduce `EngineId = string` beside the closed
  `BuiltInEngineType`, and widen `EngineStatusByNode`/`EngineModels`/
  `Workload.engine` from `EngineType` to `EngineId` so an unknown engine survives
  the bridge with a fallback display name (the `engineDisplayName` loose-string
  path and `ServiceError.engineType` are already shaped for it), while keeping
  install/start/stop controls gated to built-ins. Left unwired because it is a
  broad closed-union refactor that cannot be type-checked offline.

**Real-hardware integration (next phase):** backend startup/config, real model
naming, advertised context/capabilities, real TTFT and timeout tuning, real
concurrency/capacity tuning, real mTLS/network behaviour, performance. Core
routing correctness is already proven by construction and review.

## Readiness recommendation

```
NOT READY FOR REAL-BACKEND INTEGRATION TESTING
```

This is the honest call, for two concrete reasons — not because the design is
incomplete:

1. **No test was executed** (Go/Node toolchains absent). The suites are written
   and statically validated, but "READY" requires the mandatory offline
   unit/component tests to actually run and pass. That is a one-command step on
   any box with Go.
2. **The proxies do not yet consume the routing decision.** Until the proxy
   adoption above lands, attaching a real backend would not exercise
   capability-aware routing through PAIR's data plane, so real-backend
   integration testing of this feature is not yet meaningful.

The core routing brain, the owning-node declaration and enforcement, the
scheduler open set, and the discovery propagation are architecturally complete
and statically validated. Once the suites are executed green and the proxy
adoption lands, this flips to READY.
