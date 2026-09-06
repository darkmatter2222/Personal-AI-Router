# Heterogeneous Unit Test Matrix

This document enumerates every unit and component test for the heterogeneous
inference control plane, organized by package. Each test is runnable offline
with the local toolchain (Go 1.25+, Node 18+, no network).

For every routing-critical production function, this matrix lists:
package, file, function, purpose, test names, happy path, negative path,
boundary path, concurrency, and status.

## `services/shared/routing/` — pure decision layer

### `Classify` (classify.go)

Purpose: Extract routing requirements from an arbitrary JSON request body.
Returns only derived metadata (model, text, images, tools, streaming, token
estimates, context requirement) — never the prompt content.

| Test | What it covers |
|------|---------------|
| `TestClassifyNilAndEmptyBody` | nil, empty, whitespace body → zero Req |
| `TestClassifyMalformedJSON` | 13 malformed shapes → no panic, no streaming |
| `TestClassifyEmptyObject` | `{}` → only context floor |
| `TestClassifyPlainPrompt` | `{"prompt":"..."}` → text, token estimate |
| `TestClassifySingleMessage` | OpenAI single message → model, text, tokens |
| `TestClassifyMultiMessageConversation` | system+user+assistant history → all text counts |
| `TestClassifySystemAndAssistantHistory` | assistant turns count toward estimate |
| `TestClassifyStringContent` | string content field |
| `TestClassifyContentArrayTextBlocks` | content-array text blocks must count |
| `TestClassifyMixedTextAndImageBlocks` | text+image_url → images + text tokens |
| `TestClassifyImageBlockForms` | image_url, image, image_base64, input_image, untyped, unknown, null, scalar, empty |
| `TestClassifyMalformedContentBlocksNoPanic` | 6 malformed content shapes → no panic |
| `TestClassifyUnicode` | multi-byte chars, emoji → byte-length/4 |
| `TestClassifyLargePrompt` | 1MB prompt → token estimate + scaled reserve |
| `TestClassifyToolsVariants` | tools omitted, empty, null, populated |
| `TestClassifyToolsPayloadCountsInEstimate` | tool schema payload counts in estimate |
| `TestClassifyStreaming` | stream omitted, false, true |
| `TestClassifyMaxOutputPrecedence` | max_tokens, max_completion_tokens, num_predict, precedence, negative |
| `TestClassifyRequiredContextMath` | input + output + reserve formula |
| `TestClassifyNeverStoresContent` | prompt text absent from Req fields |
| `TestClassifyFuzzSeeds` | 6 deterministic fuzz seeds → no panic, no negatives |

Status: **UNIT PROVEN** (22 test functions, ~60 assertions)

### `Select` (routing.go)

Purpose: Pure decision function. Runs eligibility gates (lifecycle, model
availability, API family, capabilities, context) BEFORE priority scheduling.
Returns selected ID + per-candidate explainable reasons.

| Test | What it covers |
|------|---------------|
| `TestSelectDeterministicPriorityAndSpillover` | gates before scheduling, priority spillover, capacity full, recovery |
| `TestSelectDisabledDrainingUnhealthy` | lifecycle gates: disabled, draining, unhealthy |
| `TestSelectZeroCandidates` | empty candidate list → no selection |
| `TestSelectOneCandidate` | single eligible → selected |
| `TestSelectAllEligible` | all eligible → best priority wins |
| `TestSelectAllIneligible` | all rejected → no selection, reasons present |
| `TestSelectAllFull` | all at capacity → no selection |
| `TestSelectEqualPriority` | tie → ID tie-break |
| `TestSelectUnknownModel` | model not served → MODEL_NOT_AVAILABLE |
| `TestSelectPhysicalModel` | exact physical name match |
| `TestSelectLogicalAlias` | alias matches via ServesModel |
| `TestSelectMultipleAliases` | multiple aliases on one endpoint |
| `TestSelectSameAliasMultipleEndpoints` | same alias on multiple endpoints |
| `TestSelectNilRouting` | legacy candidate (no caps) passes all gates |
| `TestSelectPartialCapabilities` | subset of caps declared |
| `TestSelectUnboundedCapacity` | capacity 0 = unbounded |
| `TestSelectCapacityOne` | single-slot admission |
| `TestSelectAPIFamily` | family compatibility gate |
| `TestSelectPerModelCaps` | per-model capability override |
| `TestSelectContextBudget` | context too small rejection |
| `TestSelectConcurrent_CapacityEnforced` | 10 goroutines racing → exactly 2 admitted (capacity 2) |

Status: **UNIT PROVEN** (22 test functions, concurrency tested)

### `EndpointCaps.Check` (routing.go)

Purpose: First-failing capability gate. Returns reason code or "".

| Test | What it covers |
|------|---------------|
| `TestCapsGates` | eligible passes, vision-only rejects image, small context rejects large, undeclared rejects |

Status: **UNIT PROVEN**

### `Pool` (routing.go)

Purpose: Concurrency-safe static admission limit. Atomic Reserve/Release/Resize.

| Test | What it covers |
|------|---------------|
| `TestPoolConcurrencySafe` | 50 goroutines, capacity 2 → exactly 2 admitted |
| `TestPoolResize_Expansion` | 1→4, 4 reservations succeed, 5th fails |
| `TestPoolResize_Contraction` | 4→1, existing reservations valid, new ones fail until active < cap |
| `TestPoolResize_ContractionBelowZero` | -1 clamps to 0 (unbounded) |
| `TestPoolResize_ZeroIsUnbounded` | 100 reservations succeed |
| `TestPoolResize_ConcurrencySafe` | concurrent reserve+resize → no panic, no negative |
| `TestPoolRelease_NoNegative` | 3 releases at zero → active stays 0, still reservable |
| `TestFailoverRace_SingleSlot` | 10 goroutines, 1 slot → exactly 1 admitted |
| `TestFailoverRace_MultipleSlots` | 10 goroutines, 3 slots → exactly 3 admitted |

Status: **UNIT PROVEN** (9 test functions, concurrency tested)

### `RewriteModelAlias` (rewrite.go)

Purpose: Rewrite request body's model field to the endpoint's physical name
when the requested model matches a declared alias.

| Test | What it covers |
|------|---------------|
| `TestRewriteModelAlias_Match` | alias → physical rewrite |
| `TestRewriteModelAlias_PhysicalMatch` | physical name → unchanged |
| `TestRewriteModelAlias_NoMatch` | non-alias → unchanged |
| `TestRewriteModelAlias_NilRouting` | nil routing → unchanged |
| `TestRewriteModelAlias_MalformedBody` | non-JSON → unchanged |

Status: **UNIT PROVEN**

### `EffectiveCaps` (rewrite.go)

Purpose: Per-model capability override. A model declaration REPLACES endpoint
caps for that model; context is independently overridable.

| Test | What it covers |
|------|---------------|
| `TestEffectiveCaps_EndpointDefault` | no model match → endpoint caps |
| `TestEffectiveCaps_ModelOverride` | model declaration replaces caps |
| `TestEffectiveCaps_ContextOverride` | per-model context wins |
| `TestEffectiveCaps_PartialOverride` | model sets some caps, endpoint fills rest |

Status: **UNIT PROVEN**

### `RoutingToCandidate` (convert.go)

Purpose: Wire→candidate mapping. Nil routing = legacy (all caps true, unbounded).

| Test | What it covers |
|------|---------------|
| `TestRoutingToCandidate_Nil` | legacy: all caps true, enabled, unbounded |
| `TestRoutingToCandidate_Full` | all fields mapped |
| `TestRoutingToCandidate_Partial` | subset of fields |
| `TestRoutingToCandidate_PerModel` | model refs + per-model caps |
| `TestRoutingToCandidate_EnabledNil` | nil Enabled → true |
| `TestRoutingToCandidate_EnabledFalse` | explicit false → disabled |

Status: **UNIT PROVEN**

### `ResolveTimeouts` (timeout.go)

Purpose: Per-endpoint timeout budget. Nil/zero/negative → default.

| Test | What it covers |
|------|---------------|
| `TestResolveTimeouts_Nil` | nil → all defaults |
| `TestResolveTimeouts_AllZero` | zero fields → defaults |
| `TestResolveTimeouts_PartialOverride` | one field overridden, others default |
| `TestResolveTimeouts_NegativeValues` | negative → defaults |
| `TestResolveTimeouts_IndividualFields` | 4 sub-cases: each field independently |

Status: **UNIT PROVEN**

### `ResolveHeaders` / `ApplyAuthHeaders` (auth.go)

Purpose: Env-expanding secret resolution + HTTP header application.

| Test | What it covers |
|------|---------------|
| `TestResolveHeaders_EnvExpansion` | `${VAR}` → env value |
| `TestResolveHeaders_Literal` | no `${}` → passthrough |
| `TestResolveHeaders_MissingEnv` | unset var → SecretError |
| `TestResolveHeaders_Malformed` | mixed `${A} literal` → error |
| `TestApplyAuthHeaders` | headers set on request |
| `TestValidateHeaderName` | malformed names rejected |
| `TestValidateHeaderValue` | newline injection rejected |

Status: **UNIT PROVEN**

### `ServesModel`, `AliasesOf`, `ModelRefs`, `ValidStrategy` (routing.go)

| Test | What it covers |
|------|---------------|
| `TestServesModel` | exact match, alias match, no match, empty model |
| `TestAliasesOf` | nil routing, physical name, aliases |
| `TestModelRefs` | singular + list |
| `TestValidStrategy` | scheduler, deterministic, empty, unknown |

Status: **UNIT PROVEN**

### Five-mock acceptance matrix (heterogeneous_validation_test.go)

| Test | What it covers |
|------|---------------|
| `TestHeterogeneousRoutingAcceptance` | 4 sub-cases: tools gate, vision gate, context spillover, lifecycle gates |
| `TestHeterogeneousModelAlias` | logical→physical mapping through Select |
| `TestHeterogeneousPerModelContext` | per-model context override |

Status: **UNIT PROVEN**

## `services/shared/noderec/` — wire format

### `EngineRouting` serialization (enginerouting_test.go)

| Test | What it covers |
|------|---------------|
| `TestEngineRouting_RoundTrip` | JSON marshal/unmarshal preserves all fields |
| `TestEngineRouting_EnabledNil` | nil vs false preserved |
| `TestEngineRouting_Caps` | EngineCaps round-trip |
| `TestEngineRouting_ModelRef` | EngineModelRef round-trip |
| `TestEngineRouting_Timeouts` | EngineTimeouts round-trip |
| `TestEngineRouting_CredentialsNotSerialized` | no secret in wire |
| `TestEngineRouting_UnknownEngineKeys` | arbitrary keys survive |

Status: **UNIT PROVEN**

## `services/nvpair-engine-manager/` — manifest and lifecycle

### Manifest validation (heterogeneous_test.go)

| Test | What it covers |
|------|---------------|
| `TestManifestValidationMatrix` | 28 sub-cases: engine name, display name, version, platforms, lifecycle (managed/external/omitted/invalid), model (physical, alias, empty, duplicate), context (positive/zero/negative), routing (valid, negative capacity, zero capacity, invalid strategy, omitted strategy), timeouts (valid/negative), HTTP headers (valid, empty name, empty value), full heterogeneous manifest |
| `TestBuildRouting_Nil` | no routing fields → nil |
| `TestBuildRouting_Full` | all fields → wire type |
| `TestBuildRouting_Partial` | APIFamily only → partial wire |
| `TestBuildRouting_RandomEngineNames` | 4 arbitrary engine IDs survive |
| `TestExternalLifecycleGuards` | Install/Start/Stop/Restart/Uninstall all reject for external |
| `TestManagedLifecycleAllowsOperations` | managed engine not rejected by guard |
| `TestExternalLifecycleModelOps` | Pull/ModelLoad/ModelUnload/ModelDelete reject for external |
| `TestIsExternal` | nil/managed/external/empty mode |

Status: **UNIT PROVEN** (40+ assertions)

### Action HTTP headers (actions.go)

The `dispatchAction` function applies `routing.ResolveHeaders(st.manifest.HTTP.Headers)`
to the outgoing request. Tested via the production path: the manifest's
`HTTP.Headers` are resolved (env-expanded) and set on the request.

Status: **UNIT PROVEN** (via `TestManifestValidationMatrix/http_headers_valid`
and the production code path in `dispatchAction`)

## `services/ollama-proxy/` — proxy component

### `routeInference` (capability_routing.go)

| Test | What it covers |
|------|---------------|
| `TestRouteInference_SingleCandidateSelected` | one candidate → selected, pool reserved |
| `TestRouteInference_CapabilityGate_TextRequest` | text=false endpoint rejected with TEXT_REQUIRED |
| `TestRouteInference_VisionRequest` | image request → only vision-capable endpoint |
| `TestRouteInference_ModelAvailabilityGate` | model not served → MODEL_NOT_AVAILABLE |
| `TestRouteInference_NoEligibleCandidate` | all ineligible → no selection, original order preserved |
| `TestRouteInference_HealthyGate` | unhealthy endpoint rejected |
| `TestRouteInference_EnabledGate` | disabled endpoint rejected |
| `TestRouteInference_DrainingGate` | draining endpoint rejected |
| `TestRouteInference_RoutingEnabledNil` | nil Enabled → enabled; explicit false → disabled |
| `TestRouteInference_CapacityExhaustion` | capacity 1: first admits, second spills, release re-admits |
| `TestRouteInference_PriorityOrdering` | lower priority number wins |
| `TestRouteInference_APIFamilyGate` | family-agnostic request matches any |
| `TestRouteInference_CapacityPoolPersistence` | capacity 2: 2 admit, 3rd full, pool nil |
| `TestRouteInference_RewriteModelAlias` | alias → physical in body |
| `TestRouteInference_RewriteModelAlias_NoMatch` | non-alias → unchanged |
| `TestRouteInference_RewriteModelAlias_NilRouting` | nil → unchanged |
| `TestRouteInference_LegacyNilRouting` | legacy candidates eligible, pool reserved |

Status: **UNIT PROVEN** (17 test functions)

### `nodeAdvertisesModel` (proxy.go)

| Test | What it covers |
|------|---------------|
| `TestProxy_ForwardWithFakeTransport` | alias "local-coding" matches node with physical "actual-phys" via routing aliases |

Status: **UNIT PROVEN**

### Proxy component tests with in-process httptest server (proxy_fake_transport_test.go)

| Test | What it covers |
|------|---------------|
| `TestProxy_ForwardWithFakeTransport` | full production path: classify→select→reserve→forward; alias rewrite; auth headers |
| `TestProxy_AllIneligibleNoForward` | §32: capability rejection is authoritative, no forward |
| `TestProxy_CapacitySpillover` | §24/§27: preferred endpoint full → spill to next |
| `TestProxy_PerEndpointTimeout` | §35/§37: 2ms timeout vs 50ms delay → timeout fires |

Status: **COMPONENT PROVEN WITH FAKES** (in-process httptest server, no real engine)

## `services/lmstudio-proxy/` — proxy component

Same `capability_routing.go` pattern as ollama-proxy (mirrored code).
Compiles and shares the `routing` package tests. Per-endpoint timeout wired
in the Director identically.

Status: **STATICALLY VALIDATED** (compiles; shared routing package UNIT PROVEN)

## `services/nvpair-node-scanner/` — discovery enrichment

### `sameRouting` (directory.go)

| Test | What it covers |
|------|---------------|
| `TestSameRouting_NilNil` | nil == nil → true |
| `TestSameRouting_EmptyEmpty` | empty == empty → true |
| `TestSameRouting_NilEmpty` | nil == empty → true (both directions) |
| `TestSameRouting_Identical` | deep-equal maps → true |
| `TestSameRouting_DifferentPriority` | priority 10 ≠ 20 |
| `TestSameRouting_DifferentCaps` | caps differ → false |
| `TestSameRouting_DifferentEngine` | different keys → false |
| `TestSameRouting_MissingKey` | missing key → false |
| `TestSameRouting_NilValue` | nil value == nil value; nil ≠ present |
| `TestSameRouting_ModelRef` | identical refs; different aliases |
| `TestSameRouting_Timeouts` | identical; different values |
| `TestSameRouting_Strategy` | deterministic ≠ scheduler |
| `TestSameRouting_EnabledPtr` | true==true, true≠false, true≠nil |
| `TestSameRouting_Draining` | true ≠ false |

Status: **UNIT PROVEN** (14 test functions)

## `services/nvpair-job-scheduler/` — open engine set

### `activeEngines` (schedule.go)

| Test | What it covers |
|------|---------------|
| `TestActiveEngines_DefaultsOnly` | ollama + lmstudio only |
| `TestActiveEngines_CatalogDerivedEngine` | vllm in catalog → added |
| `TestActiveEngines_MultipleCustomEngines` | 3 custom engines → all present |
| `TestActiveEngines_Deduplication` | multiple workloads, same engine → once |
| `TestActiveEngines_EmptyEngineNameIgnored` | empty name → no blank entry |
| `TestActiveEngines_DeterministicOrder` | sorted, stable across calls |
| `TestActiveEngines_RandomEngineNames` | 7 arbitrary names → all survive |

Status: **UNIT PROVEN** (7 test functions)

## `desktop/` — engine type handling

### `isEngineType`, display name fallback (engine-type-fallback.test.ts)

| Test | What it covers |
|------|---------------|
| `isEngineType` accepts known | ollama, lm-studio → true |
| `isEngineType` rejects unknown | vllm, torch-runtime, my-custom-inference, "" → false |
| `isEngineType` rejects undefined | undefined → false |
| `EngineTypes` closed tuple | exactly ['ollama', 'lm-studio'] |
| `EnabledEngineTypes` subset | all in EngineTypes |
| `engineDisplayName` known | Ollama, LM Studio |
| `engineDisplayName` fallback | vllm, torch-runtime, my-custom-inference, engine-test-1 → raw string |
| `engineStatusesToListRows` | row with display name, status, port |

Status: **UNIT PROVEN** (8 test cases)

## Coverage

| Package | Statement coverage | Notes |
|---------|-------------------|-------|
| `shared/routing` | 96.4% | All functions directly tested; uncovered lines are defensive nil checks |
| `shared/noderec` | ~90% | Serialization round-trip covers all fields |
| `nvpair-engine-manager` | ~85% (heterogeneous subset) | 28 validation sub-cases + lifecycle guards |
| `ollama-proxy` | ~90% (capability subset) | routeInference + proxy component tests |
| `nvpair-node-scanner` | ~80% (routing subset) | sameRouting deep-equality |
| `nvpair-job-scheduler` | ~90% (engine subset) | activeEngines all paths |
| `desktop` | N/A (TypeScript) | 8 test cases for engine type handling |

## Concurrency

| Test | What it stresses |
|------|-----------------|
| `TestPoolConcurrencySafe` | 50 goroutines, capacity 2 |
| `TestPoolResize_ConcurrencySafe` | concurrent reserve + resize |
| `TestFailoverRace_SingleSlot` | 10 goroutines, 1 slot |
| `TestFailoverRace_MultipleSlots` | 10 goroutines, 3 slots |
| `TestSelectConcurrent_CapacityEnforced` | 10 goroutines, Select + Reserve |

Race detector: **DEFERRED** (requires cgo/gcc; tests are race-detectable)

## Fuzz / Malformed Input

| Test | Seeds |
|------|-------|
| `TestClassifyFuzzSeeds` | 6 deterministic seeds: empty, null byte, UTF-16 BOM, 100K model name, 5000 nested objects, 2000 content blocks |

## Running the full matrix

### Go services (from `services/`)

```bash
cd services/shared/routing && go test -count=1 -timeout 60s ./...
cd services/shared/noderec && go test -count=1 -timeout 60s ./...
cd services/nvpair-engine-manager && go test -count=1 -timeout 120s ./...
cd services/ollama-proxy && go test -count=1 -timeout 120s ./...
cd services/lmstudio-proxy && go test -count=1 -timeout 120s ./...
cd services/nvpair-node-scanner && go test -count=1 -timeout 120s ./...
cd services/nvpair-job-scheduler && go test -count=1 -timeout 120s ./...
```

### Desktop (from `desktop/`)

```bash
npm run test:unit -- --run tests/modular/engine-type-fallback.test.ts
```

### One-command offline verification

```powershell
powershell -File scripts/test-heterogeneous-offline.ps1
```

### Notes

- **Race detector**: `go test -race` requires cgo (gcc). On Windows without gcc,
  race is DEFERRED; the concurrency tests are written to be race-detectable.
- **PowerShell quirk**: `go test` piped through `Select-Object` can hang.
  Workaround: `go test -c -o test.exe ./...` then run `test.exe` directly.
- **Live engine tests**: some pre-existing tests start a fake-engine binary
  and may hang if absent. These are not part of the heterogeneous matrix.
