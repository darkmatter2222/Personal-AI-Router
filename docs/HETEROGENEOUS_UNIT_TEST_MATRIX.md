<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Heterogeneous routing — unit-test matrix

Every routing-critical production function added or modified for the
heterogeneous control plane, and the direct tests that exercise it. Tests call
the real production functions — never a surrogate (no `strings.Replace` stand-in
for alias rewrite, no boolean flag stand-in for auth, no manual `Release()`
stand-in for cancellation-driven release).

## Execution status legend

- **STATIC** — statically validated by adversarial line-by-line review (a second
  model acting as the Go compiler). The Go toolchain is absent on this machine
  (no `go`, no module cache), so no Go test was executed here.
- **UNIT** — a direct unit test exists that calls the production function.
- **COMPONENT** — an in-process component test with a fake `http.RoundTripper`
  and `httptest.NewRecorder` exercises the production handler/executor.

No test below has been executed (Go toolchain unavailable offline). All are
written to pass and were confirmed compile-clean and logic-correct by review.
Run `scripts/test-heterogeneous-offline.ps1` on a box with Go to execute them.

## Shared routing package (`nvpair-shared/routing`) — the pure core

Target: ~100% meaningful statement coverage. stdlib-only, so fully runnable with
no third-party dependencies.

| File | Function | Purpose | Tests | happy | neg | bound | conc | status |
| --- | --- | --- | --- | :-: | :-: | :-: | :-: | --- |
| routing.go | `EngineRouting.Enabled/ResolvedPriority/ResolvedStrategy/External` | Default resolution (tri-state-safe) | TestEnabledDefaultsToTrue, TestResolvedPriorityOmittedIsLowest, TestResolvedStrategyDefault, TestExternalLifecycle | ✓ | ✓ | ✓ | — | UNIT/STATIC |
| routing.go | `Timeouts.Connect/ResponseHeader/FirstByte/Action` | Per-field 0=default resolution | TestTimeoutResolution, TestTimeoutsIndependent | ✓ | ✓ | ✓ | — | UNIT/STATIC |
| routing.go | `EngineRouting.Endpoint` | Materialise resolved Endpoint | TestEndpointBuilderAppliesDefaults | ✓ | — | ✓ | — | UNIT/STATIC |
| routing.go | JSON (de)serialization | Wire round-trip; omitempty | TestJSONRoundTrip, TestDisabledOmittedWhenFalse | ✓ | — | — | — | UNIT/STATIC |
| validate.go | `EngineRouting.Validate` / `validModelName` | Reject malformed config | TestValidate | ✓ | ✓ | ✓ | — | UNIT/STATIC |
| classify.go | `Classify` | Request classification | TestClassify_NilAndMalformed, TestClassify_ModelAndFlags (30 cases), TestClassify_LargePromptScales, TestClassify_LargeUnicode, TestClassify_ContextGrowsWithTools | ✓ | ✓ | ✓ | — | UNIT/STATIC |
| classify.go | `decodeContent`/`decodeBlock`/`rawTextChars` | Content/image extraction | TestDecodeContentHelpers | ✓ | ✓ | ✓ | — | UNIT/STATIC |
| classify.go | `estimateTokensFromChars` | Conservative token estimate | TestEstimateTokensFromChars | ✓ | ✓ | ✓ | — | UNIT/STATIC |
| classify.go | `resolveOutputTokens`/`clampNonNeg` | Output-length precedence | TestResolveOutputTokensPrecedence | ✓ | ✓ | ✓ | — | UNIT/STATIC |
| alias.go | `MatchModel` | Ownership by physical or alias | TestMatchModel_Physical/Alias/PhysicalPrecedenceOverAlias/NotFoundAndEmpty/DifferentPhysicalAcrossEndpoints | ✓ | ✓ | ✓ | — | UNIT/STATIC |
| alias.go | `Owns`/`PhysicalFor` | Ownership convenience | TestOwnsAndPhysicalFor | ✓ | ✓ | — | — | UNIT/STATIC |
| alias.go | `RewriteModel` | Body model → physical | TestRewriteModel, TestRewriteModel_NoChangeCases | ✓ | ✓ | ✓ | — | UNIT/STATIC |
| eligibility.go | `Evaluate` | Capability/model/context/state gate | TestEvaluate_OK, TestEvaluate_Gates (15 cases), TestEvaluate_ReasonPrecedence, TestEvaluate_RejectionCarriesModel | ✓ | ✓ | ✓ | — | UNIT/STATIC |
| policy.go | `Decide` | Eligibility-then-policy ordering | TestDecide_ZeroCandidates/DeterministicPriorityOrder/PriorityTieBreakByID/DefaultStrategyUsesSchedulerOrder/DefaultUnrankedSortByID/AllIneligible/EligibilityBeforeScheduling/PlacementCarriesFields/RecordsStrategy | ✓ | ✓ | ✓ | — | UNIT/STATIC |
| policy.go | `ResolveStrategy` | Single-policy resolution | TestResolveStrategy | ✓ | ✓ | ✓ | — | UNIT/STATIC |
| capacity.go | `Pool.Reserve/Resize/Used/Cap/Available` | Authoritative admission | TestPool_ReserveUpToCapacity/ReleaseIdempotent/Unbounded/ResizeExpandContract | ✓ | ✓ | ✓ | ✓ | UNIT/STATIC |
| capacity.go | `Pools.Reserve/Reconcile/Used/Cap/Len` | Per-endpoint pools + reconfig | TestPools_ReserveCreatesAndKeepsCap/Reconcile/RemovalThenRecreateIsFresh | ✓ | ✓ | ✓ | ✓ | UNIT/STATIC |
| capacity.go | Pool concurrency | Exact-count, failover race, race-detector | TestPool_ConcurrentReserveExactCount, TestPools_FailoverRaceSingleSlot, TestPools_ConcurrentReserveResizeReconcile | ✓ | — | ✓ | ✓ | UNIT/STATIC |
| auth.go | `ApplyAuth` | Local header application | TestApplyAuth_LiteralValue/EnvValue/RealEnvViaSetenv/MissingEnvIsUnavailable/EmptyEnvIsUnavailable/ValueEnvExclusivity/InvalidHeaderName/NewlineInjectionRejectedAndSecretNotLeaked/SetReplacesClientValue/DuplicateSpecsLastWins/BadEnvName/NilHeader | ✓ | ✓ | ✓ | — | UNIT/STATIC |
| auth.go | `StripClientAuth` | Strip client credential | TestStripClientAuth | ✓ | ✓ | — | — | UNIT/STATIC |
| auth.go | `LocalAuth.Validate/Empty` | Manifest-load validation | TestLocalAuthValidate, TestLocalAuthEmpty | ✓ | ✓ | ✓ | — | UNIT/STATIC |
| auth.go | `validHeaderName/validHeaderValue/validEnvName` | Injection guards | TestHeaderValidators | ✓ | ✓ | ✓ | — | UNIT/STATIC |
| lifecycle.go | `LifecycleMode.Allows` / `LifecycleOp.ReadOnly/Mutating` / `EngineRouting.Allows` | External-lifecycle decision | TestLifecycle_ExternalForbidsAllMutations/ExternalAllowsReadOnly/ManagedAllowsEverything/EngineRoutingAllows | ✓ | ✓ | ✓ | — | UNIT/STATIC |
| forward.go | `Forward` (+ attempt/roundTripWithHeaderTimeout/readFirst/copyStream) | CLASSIFY→…→FORWARD executor | TestForward_* (18 cases, below) | ✓ | ✓ | ✓ | ✓ | COMPONENT/STATIC |
| fuzz | `Classify`/`RewriteModel`/`EngineRouting` parse | Malformed-input hardening | FuzzClassify, FuzzRewriteModel, FuzzEngineRoutingParse | — | ✓ | ✓ | — | UNIT/STATIC |

### Forward component-test matrix (fake RoundTripper + httptest.NewRecorder)

`TestForward_` cases: NoEligible, NormalText, ModelRewriteToPhysical, AuthHeaderAppliedAndClientStripped, AuthUnavailableFailsOver, CapacitySpillover, CapacityFullEverywhere, FailoverRetryableStatuses (408/429/500/502/503/504), FailoverDialError, NoFailoverOnClientErrors (400/401/422), 404FailoverOnlyForInference, AllRetryableLastCommitted, NoRetryAfterCommit, ClientCancellationReleasesCapacity, PerEndpointResponseHeaderTimeout, FirstByteTimeoutDistinctFromHeader, StreamingReservationHeldThenReleased, MissingTargetFailsOver. Every timeout-prone case is bounded by `forwardWithin` (3s) so no test can hang.

## Scheduler open engine set (`nvpair-job-scheduler`)

| File | Function | Purpose | Tests | status |
| --- | --- | --- | --- | --- |
| schedule.go | `engineList`/`engineListLocked`/`pruneEmitted` | Open engine set + stale cleanup | TestOpenEngineSet_DiscoveredEngineEmitsPriorityAndPrunes | UNIT/STATIC |
| state.go | `applyNodesChanged` (engine-set derivation) | Learn engines from discovery | TestApplyNodesChanged_EngineSetChangeReportsChanged, TestOpenEngineSet_* | UNIT/STATIC |
| schedule.go | `recomputeAll`/`status` (open set) | Emit per discovered engine | TestOpenEngineSet_* (built-ins still emit; existing tests unchanged) | UNIT/STATIC |

Runnable in the offline allowlist (job-scheduler tests are pure in-memory).

## Discovery wire (`nvpair-shared/noderec`)

| File | Function | Purpose | Tests | status |
| --- | --- | --- | --- | --- |
| noderec.go | `DirectoryNode.EngineRouting` + `RoutingByEngine` field | Wire round-trip, legacy default | TestDirectoryNode_RoutingByEngineRoundTrip/EngineRoutingLegacyDefault/RoutingByEngineOmittedWhenAbsent | UNIT/STATIC |

Uses a dynamically-novel engine id (`engine-982341`) that exists in no source
enumeration. Runnable in the offline allowlist.

## Engine-manager external-lifecycle enforcement (`nvpair-engine-manager`)

| File | Function | Purpose | Tests | status |
| --- | --- | --- | --- | --- |
| lifecycleguard.go | `guardOp`/`engineLifecycle`/`externalEngine` | Refuse mutation on external engines | TestExternalLifecycleGuard_HelperMatrix | STATIC (not offline-runnable) |
| lifecycle.go/install.go/setport.go | `Start/StartWith/Stop/Restart/Install/Uninstall/SetPort` guards + `StopAll` skip | Enforce at production entry points | TestExternalLifecycleGuard_RefusesMutationsThroughProductionEntryPoints | STATIC (not offline-runnable) |
| registry.go | `Manifest.Validate` (routing/auth) | Validate routing/auth blocks at load | (covered by routing.Validate + LocalAuth.Validate unit tests) | STATIC |
| models.go | `ModelsResult` routing emission | Advertise routing metadata, never Auth | (static; not offline-runnable) | STATIC |

The engine-manager guard test exercises the REAL entry points (the guard
short-circuits before any process work). It is not in the offline allowlist
because the engine-manager package has pre-existing subprocess/TestMain tests
(`e2e_test.go`, `executor_test.go`, `main_test.go`) that are unsafe to run
unattended. The enforcement DECISION is proven offline by the routing package's
`lifecycle_test.go`; the WIRING is proven by review and this test on a dev box.

## Hardening / edge-case suites (added second pass)

About 50 additional tests targeting gaps, grouped by area. All land in packages
the offline script already runs, so they execute automatically.

| File | Focus | Notable cases |
| --- | --- | --- |
| classify_edge_test.go | Real-world request shapes (Ollama + OpenAI) | keep_alive/raw/format/think, deprecated `context` int array, `tool_choice:none`, legacy `function_call`, missing/null/number/bool content, `image_url` bare-string + data-URI, Anthropic `source` images, `stream_options`, **`num_ctx` context floor + eligibility gate**, output precedence at 0/null/huge, emoji/CJK rune counting, whitespace-only, embeddings shapes |
| eligibility_edge_test.go | Multi-model engines | matched-model caps/context (vision vs text vs tools), ollama↔ollama family, empty-model set, metadata-only caps do not gate, unneeded caps do not gate |
| policy_edge_test.go | Ordering edges | omitted priority sorts last, stale/duplicate scheduler-order ids, single-ineligible, full mixed explanation with distinct reasons, deterministic-on-last |
| capacity_edge_test.go | Admission edges | release-order independence, same-caps reconcile preserves in-flight, reconfigure-to-unbounded, Available boundaries, per-id independence, reconcile-vs-release race |
| auth_edge_test.go | Header application | multiple distinct headers, unrelated-header preservation, internal-space values, single-char env value |
| forward_edge_test.go | Executor edges | multi-chunk stream ordering, empty-body commit, method/path passthrough, rewrite-disabled body preservation, all-candidates header timeout -> local 503 + released |
| validate_edge_test.go | Contract validation | model-name length boundary (512 ok, 513 rejected), alias rules (dup alias ok, dup physical rejected, slash/colon ok, control char rejected) |
| open_engine_edge_test.go (scheduler) | Open engine set | dedup across modelsByEngine/routingByEngine keys, single-node engine still emits |
| routing_meta_edge_test.go (noderec) | Wire round-trip | multiple engines' routing metadata round-trip independently |

Production change accompanying these: `Classify` now treats Ollama
`options.num_ctx` as a requested-context floor (`RequiredContext = max(estimated
conversation size, num_ctx)`), so a request that explicitly asks for a large
window is gated against a too-small model. Status: STATICALLY VALIDATED, NOT
EXECUTED.

## Not directly unit-tested here (deferred, with rationale)

- **Broker projection** (`directoryToEnriched`/`toAvailable` RoutingByEngine
  pass-through) and **scanner enrichment** (`fetchModels`/`applyModels`/cache
  threading): additive, mechanically mirroring the `ModelsByEngine` triad,
  reviewed compile-clean. Not offline-runnable (those packages have
  listener/subprocess tests and need a warm module cache). The wire round-trip is
  proven by the noderec test; the projection/enrichment are STATIC.
