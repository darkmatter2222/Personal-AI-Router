# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

<#
.SYNOPSIS
  Deterministic, offline, unattended validation for the heterogeneous
  capability-aware routing work, with two modes.

.PARAMETER Mode
  Developer (default) — skips are allowed and clearly reported; the script exits
    nonzero only when a check that RAN failed. Useful on a box without the full
    toolchain.
  Readiness — every MANDATORY check MUST execute and pass. A missing Go
    toolchain, a skipped mandatory suite, an unavailable race detector, or a
    sub-threshold package coverage all FAIL. A NON-mandatory skip (e.g. desktop
    node_modules absent when no desktop code changed) is informational and never
    fails readiness. This is the gate for declaring the branch READY FOR
    REAL-BACKEND INTEGRATION TESTING.

.DESCRIPTION
  Guarantees in BOTH modes (by construction):
    * Installs nothing (no go get / npm install / winget / choco). A missing
      toolchain FAILS with "REQUIRED TOOLCHAIN MISSING" rather than fetching it.
    * Touches no network (GOPROXY=off).
    * Launches no inference engine, service binary, or full deployment.
    * Runs NO cross-process integration tests: never `go test ./...` at the
      services root, never services/tests, never `-tags live`. It runs an
      explicit allowlist of pure/unit packages only.
    * Requires no interaction.

  Exit 0 = success for the selected mode. Exit 1 = failure.
#>

param(
    [ValidateSet('Developer', 'Readiness')]
    [string]$Mode = 'Developer'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$Readiness = ($Mode -eq 'Readiness')

# Force offline behavior for every child process (process-scoped only).
$env:GOFLAGS = '-mod=mod'
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:GOTOOLCHAIN = 'local'
$env:HF_HUB_OFFLINE = '1'
$env:TRANSFORMERS_OFFLINE = '1'
$env:npm_config_offline = 'true'
$env:npm_config_audit = 'false'
$env:npm_config_fund = 'false'

$RepoRoot = Split-Path -Parent $PSScriptRoot
$Services = Join-Path $RepoRoot 'services'
$Desktop  = Join-Path $RepoRoot 'desktop'

# Package statement-coverage gates (Readiness mode). Package-specific, meaningful
# targets rather than one repository-wide number.
$RoutingCoverageMin = 98.0
$RouteAdapterCoverageMin = 88.0

$script:Failures = 0
$script:Ran = 0
$script:Skipped = 0

function Section($n) { Write-Host ""; Write-Host "==== $n ($Mode mode) ====" -ForegroundColor Cyan }
function Pass($m) { Write-Host "PASS: $m" -ForegroundColor Green; $script:Ran++ }
function Fail($m) { Write-Host "FAIL: $m" -ForegroundColor Red; $script:Failures++; $script:Ran++ }
function Have($c) { return [bool](Get-Command $c -ErrorAction SilentlyContinue) }

# Skip: in Developer mode it is a reported non-failure; in Readiness mode a skip
# of a MANDATORY check is a failure.
function SkipOrFail($m, [bool]$Mandatory = $true) {
    if ($Readiness -and $Mandatory) {
        Fail "REQUIRED CHECK NOT EXECUTED: $m"
    } else {
        Write-Host "SKIPPED: $m" -ForegroundColor Yellow
        $script:Skipped++
    }
}

function Go-Test($moduleDir, [string[]]$packages, $label) {
    Push-Location $moduleDir
    try {
        Write-Host "> (cd $moduleDir) go test $($packages -join ' ')"
        & go test @packages
        if ($LASTEXITCODE -eq 0) { Pass $label } else { Fail "$label (go test exit $LASTEXITCODE)" }
    } finally { Pop-Location }
}

Section "Toolchain detection"
$HasGo = Have 'go'
$HasGofmt = Have 'gofmt'
$HasNode = Have 'node'
$HasNodeModules = Test-Path (Join-Path $Desktop 'node_modules')
Write-Host "go=$HasGo gofmt=$HasGofmt node=$HasNode node_modules=$HasNodeModules"

if ($Readiness -and -not $HasGo) {
    Write-Host ""
    Write-Host "REQUIRED TOOLCHAIN MISSING: Go is not on PATH. Readiness mode cannot" -ForegroundColor Red
    Write-Host "install it (offline) and cannot pass without executing the Go suites." -ForegroundColor Red
    Write-Host "RESULT: FAIL" -ForegroundColor Red
    exit 1
}

# ---- gofmt (routing package) ------------------------------------------------
Section "gofmt (routing package)"
if ($HasGofmt) {
    $bad = & gofmt -l (Join-Path $Services 'shared/routing')
    if ([string]::IsNullOrWhiteSpace($bad)) { Pass "routing gofmt clean" } else { Fail "gofmt unformatted:`n$bad" }
} else {
    SkipOrFail "gofmt not on PATH" $true
}

# ---- Go unit/component tests (explicit safe allowlist) ----------------------
Section "Go unit/component tests (allowlist)"
if ($HasGo) {
    Go-Test (Join-Path $Services 'shared') @('./routing/...','./routeadapter/...','./noderec/...','./schedulerwire/...') 'shared routing/routeadapter/noderec/schedulerwire'
    Go-Test (Join-Path $Services 'nvpair-job-scheduler') @('./...') 'nvpair-job-scheduler'
    # Engine-manager critical, PURE logic only: manifest validation (external/adopt
    # runtime mode, action contradictions) and the external-lifecycle action guard.
    # The -run pattern selects ONLY these dependency-light tests — none spawn a
    # process, open a socket or touch the network. The package TestMain compiles
    # helper binaries (offline; no engine launch), so this installs/starts nothing.
    Go-Test (Join-Path $Services 'nvpair-engine-manager') @('-run','TestExternalRuntime|TestManagedProcess|TestAction_|TestExternalEngine|TestRuntimeExternal','.') 'engine-manager manifest-validation + lifecycle-guard (pure subset)'
} else {
    SkipOrFail "go not on PATH — Go unit tests not executed" $true
}

# ---- Race detector (routing package) ----------------------------------------
Section "Go race detector (routing package)"
if ($HasGo) {
    Push-Location (Join-Path $Services 'shared')
    try {
        Write-Host "> go test -race ./routing/..."
        & go test -race ./routing/...
        if ($LASTEXITCODE -eq 0) { Pass "routing -race" }
        else { Fail "routing -race (exit $LASTEXITCODE; needs a working cgo/C toolchain)" }
    } finally { Pop-Location }
} else {
    SkipOrFail "go not on PATH — race tests not executed" $true
}

# ---- Coverage gate (routing package) ----------------------------------------
Section "Go coverage (routing package, min $RoutingCoverageMin`%)"
if ($HasGo) {
    Push-Location (Join-Path $Services 'shared')
    try {
        $cover = Join-Path ([System.IO.Path]::GetTempPath()) 'pair-routing-cover.out'
        Write-Host "> go test -coverprofile routing coverage"
        & go test -coverprofile="$cover" ./routing/...
        if ($LASTEXITCODE -ne 0) {
            Fail "coverage run failed (exit $LASTEXITCODE)"
        } else {
            $func = & go tool cover -func="$cover"
            $totalLine = $func | Where-Object { $_ -match 'total:' } | Select-Object -Last 1
            if ($totalLine -match '([\d.]+)%') {
                $pct = [double]$Matches[1]
                Write-Host "routing total coverage: $pct`%"
                if ($pct -ge $RoutingCoverageMin) { Pass "routing coverage $pct% >= $RoutingCoverageMin%" }
                else { Fail "routing coverage $pct% < $RoutingCoverageMin%" }
            } else {
                Fail "could not parse routing coverage total"
            }
            Remove-Item $cover -ErrorAction SilentlyContinue
        }
    } finally { Pop-Location }
} else {
    SkipOrFail "go not on PATH — coverage not measured" $true
}

# ---- Coverage gate (routeadapter package) -----------------------------------
Section "Go coverage (routeadapter package, min $RouteAdapterCoverageMin`%)"
if ($HasGo) {
    Push-Location (Join-Path $Services 'shared')
    try {
        $cover = Join-Path ([System.IO.Path]::GetTempPath()) 'pair-routeadapter-cover.out'
        Write-Host "> go test -coverprofile routeadapter coverage"
        & go test -coverprofile="$cover" ./routeadapter/...
        if ($LASTEXITCODE -ne 0) {
            Fail "routeadapter coverage run failed (exit $LASTEXITCODE)"
        } else {
            $func = & go tool cover -func="$cover"
            $totalLine = $func | Where-Object { $_ -match 'total:' } | Select-Object -Last 1
            if ($totalLine -match '([\d.]+)%') {
                $pct = [double]$Matches[1]
                Write-Host "routeadapter total coverage: $pct`%"
                if ($pct -ge $RouteAdapterCoverageMin) { Pass "routeadapter coverage $pct% >= $RouteAdapterCoverageMin%" }
                else { Fail "routeadapter coverage $pct% < $RouteAdapterCoverageMin%" }
            } else {
                Fail "could not parse routeadapter coverage total"
            }
            Remove-Item $cover -ErrorAction SilentlyContinue
        }
    } finally { Pop-Location }
} else {
    SkipOrFail "go not on PATH — routeadapter coverage not measured" $true
}

# ---- Desktop typecheck + unit tests -----------------------------------------
Section "Desktop typecheck + unit tests"
if ($HasNode -and $HasNodeModules) {
    Push-Location $Desktop
    try {
        Write-Host "> npm run typecheck"; & npm run typecheck
        if ($LASTEXITCODE -eq 0) { Pass "desktop typecheck" } else { Fail "desktop typecheck (exit $LASTEXITCODE)" }
        Write-Host "> npm run test:unit"; & npm run test:unit
        if ($LASTEXITCODE -eq 0) { Pass "desktop test:unit" } else { Fail "desktop test:unit (exit $LASTEXITCODE)" }
    } finally { Pop-Location }
} else {
    # Desktop deps are only mandatory in Readiness mode IF desktop sources changed.
    $desktopChanged = $false
    if (Have 'git') {
        Push-Location $RepoRoot
        try { $desktopChanged = [bool]((& git diff --name-only main...HEAD 2>$null) | Where-Object { $_ -like 'desktop/*' }) } catch {} finally { Pop-Location }
    }
    SkipOrFail "desktop/node_modules absent (not installing offline) — desktop tests not executed" $desktopChanged
}

# ---- Summary ----------------------------------------------------------------
Section "Summary"
# A skipped MANDATORY check is already counted as a FAILURE (see SkipOrFail), so
# in readiness mode Failures==0 guarantees every mandatory check executed and
# passed. Skipped holds only NON-mandatory skips, which never fail readiness.
Write-Host "ran=$($script:Ran) failures=$($script:Failures) skipped=$($script:Skipped) (skipped are non-mandatory)"
if ($script:Failures -gt 0) {
    Write-Host "RESULT: FAIL" -ForegroundColor Red
    exit 1
}
if ($Readiness) {
    Write-Host "RESULT: PASS (readiness)" -ForegroundColor Green
    exit 0
}
if ($script:Ran -eq 0) {
    Write-Host "RESULT: NO CHECKS EXECUTED (toolchains unavailable). Nothing failed, nothing proven." -ForegroundColor Yellow
    exit 0
}
Write-Host "RESULT: PASS (developer)" -ForegroundColor Green
exit 0
