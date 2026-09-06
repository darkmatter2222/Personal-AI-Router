# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

<#
.SYNOPSIS
  Deterministic, offline, unattended validation for the heterogeneous
  capability-aware routing work.

.DESCRIPTION
  This script runs ONLY the safe, deterministic unit/component tests for the
  heterogeneous routing feature. It is designed to be run unattended with no
  network, no installs, and no chance of launching a real engine or the full
  PAIR system.

  Guarantees / non-goals (by construction):
    * Installs nothing (no go get / npm install / winget / choco).
    * Touches no network: Go module access is forced offline (GOPROXY=off).
    * Launches no inference engine, no service binary, and no full deployment.
    * Runs NO cross-process integration tests: it never runs `go test ./...`,
      never touches services/tests, and never uses `-tags live`. It runs an
      explicit allowlist of pure/unit packages only.
    * Requires no interaction.

  Exit codes:
    0  every check that could run passed (checks whose toolchain is absent are
       reported as SKIPPED, which is not a failure on an offline machine).
    1  a check that ran FAILED (a real test failure or a format violation).

  It is safe to run on a developer box too: there it actually executes the Go
  and desktop suites; on a toolchain-less machine it reports each as SKIPPED.
#>

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# Force offline behavior for every child process this script spawns. These are
# process-scoped only; no global configuration is modified.
$env:GOFLAGS   = '-mod=mod'
$env:GOPROXY   = 'off'
$env:GOSUMDB   = 'off'
$env:GOTOOLCHAIN = 'local'
$env:HF_HUB_OFFLINE = '1'
$env:TRANSFORMERS_OFFLINE = '1'
$env:npm_config_offline = 'true'
$env:npm_config_audit = 'false'
$env:npm_config_fund = 'false'

$RepoRoot = Split-Path -Parent $PSScriptRoot
$Services = Join-Path $RepoRoot 'services'
$Desktop  = Join-Path $RepoRoot 'desktop'

$script:Failures = 0
$script:Ran      = 0
$script:Skipped  = 0

function Section($name) { Write-Host ""; Write-Host "==== $name ====" -ForegroundColor Cyan }
function Skip($msg)     { Write-Host "SKIPPED: $msg" -ForegroundColor Yellow; $script:Skipped++ }
function Pass($msg)     { Write-Host "PASS: $msg" -ForegroundColor Green; $script:Ran++ }
function Fail($msg)     { Write-Host "FAIL: $msg" -ForegroundColor Red; $script:Failures++; $script:Ran++ }

function Have($cmd) { return [bool](Get-Command $cmd -ErrorAction SilentlyContinue) }

# Run `go test` for an explicit, safe package list inside one module directory.
function Go-Test($moduleDir, [string[]]$packages, $label) {
    Push-Location $moduleDir
    try {
        Write-Host "> (cd $moduleDir) go test $($packages -join ' ')"
        & go test @packages
        if ($LASTEXITCODE -eq 0) { Pass $label } else { Fail "$label (go test exit $LASTEXITCODE)" }
    } finally {
        Pop-Location
    }
}

Section "Toolchain detection"
$HasGo = Have 'go'
$HasGofmt = Have 'gofmt'
$HasNode = Have 'node'
$HasNodeModules = Test-Path (Join-Path $Desktop 'node_modules')
Write-Host "go:            $HasGo"
Write-Host "gofmt:         $HasGofmt"
Write-Host "node:          $HasNode"
Write-Host "node_modules:  $HasNodeModules"

# ---- Go: gofmt (format) on the routing package only -------------------------
Section "gofmt (routing package)"
if ($HasGofmt) {
    $routingDir = Join-Path $Services 'shared/routing'
    $bad = & gofmt -l $routingDir
    if ([string]::IsNullOrWhiteSpace($bad)) { Pass "routing gofmt clean" }
    else { Fail "gofmt found unformatted files:`n$bad" }
} else {
    Skip "gofmt not on PATH"
}

# ---- Go: safe unit/component test allowlist ---------------------------------
# ONLY pure/unit packages. Never `./...` at the services root, never
# services/tests, never -tags live.
Section "Go unit/component tests (allowlist)"
if ($HasGo) {
    # The new shared routing package is stdlib-only and fully self-contained.
    Go-Test (Join-Path $Services 'shared') @('./routing/...','./noderec/...','./schedulerwire/...') 'shared routing/noderec/schedulerwire'
    # Job scheduler: schedule_test.go + telemetry_test.go are pure in-memory.
    Go-Test (Join-Path $Services 'nvpair-job-scheduler') @('./...') 'nvpair-job-scheduler'
} else {
    Skip "go not on PATH — Go tests not executed (documented offline limitation)"
}

# ---- Go: race detector on the routing package (best effort) -----------------
Section "Go race detector (routing package)"
if ($HasGo) {
    Push-Location (Join-Path $Services 'shared')
    try {
        Write-Host "> go test -race ./routing/..."
        & go test -race ./routing/...
        if ($LASTEXITCODE -eq 0) { Pass "routing -race" }
        else { Fail "routing -race (exit $LASTEXITCODE)" }
    } catch {
        Skip "race detector unavailable (needs cgo/gcc): $($_.Exception.Message)"
    } finally {
        Pop-Location
    }
} else {
    Skip "go not on PATH — race tests not executed"
}

# ---- Desktop: typecheck + unit tests (only if deps already present) ---------
Section "Desktop typecheck + unit tests"
if ($HasNode -and $HasNodeModules) {
    Push-Location $Desktop
    try {
        Write-Host "> npm run typecheck"
        & npm run typecheck
        if ($LASTEXITCODE -eq 0) { Pass "desktop typecheck" } else { Fail "desktop typecheck (exit $LASTEXITCODE)" }
        Write-Host "> npm run test:unit"
        & npm run test:unit
        if ($LASTEXITCODE -eq 0) { Pass "desktop test:unit" } else { Fail "desktop test:unit (exit $LASTEXITCODE)" }
    } finally {
        Pop-Location
    }
} elseif ($HasNode) {
    Skip "desktop/node_modules absent — not installing (offline). Desktop tests not executed."
} else {
    Skip "node not on PATH — desktop tests not executed"
}

# ---- Summary ----------------------------------------------------------------
Section "Summary"
Write-Host "ran=$($script:Ran)  failures=$($script:Failures)  skipped=$($script:Skipped)"
if ($script:Failures -gt 0) {
    Write-Host "RESULT: FAIL" -ForegroundColor Red
    exit 1
}
if ($script:Ran -eq 0) {
    Write-Host "RESULT: NO CHECKS EXECUTED (all toolchains unavailable offline). Nothing failed, but nothing was proven." -ForegroundColor Yellow
    exit 0
}
Write-Host "RESULT: PASS" -ForegroundColor Green
exit 0
