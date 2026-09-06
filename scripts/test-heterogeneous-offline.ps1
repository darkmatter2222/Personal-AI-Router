# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# test-heterogeneous-offline.ps1 — builds and tests every heterogeneous
# inference control plane component offline (no network, no GPU, no live
# engine). Run from the repository root:
#
#   powershell -File scripts/test-heterogeneous-offline.ps1

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
$services = Join-Path $root 'services'
$desktop = Join-Path $root 'desktop'

# --- Go environment ---
$goBin = Join-Path $env:USERPROFILE 'sdk\go\bin'
if (Test-Path $goBin) {
    $env:PATH = "$goBin;$env:PATH"
}
$env:GOTMPDIR = Join-Path $env:TEMP 'go-tmp'
New-Item -ItemType Directory -Force -Path $env:GOTMPDIR | Out-Null
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'

$pass = 0
$fail = 0
$failures = @()

function Test-GoComponent {
    param([string]$Name, [string]$Dir)
    Write-Host "`n=== $Name ===" -ForegroundColor Cyan
    Push-Location $Dir
    try {
        Write-Host 'Building...'
        $buildOut = & go build ./... 2>&1
        if ($LASTEXITCODE -ne 0) {
            Write-Host $buildOut
            Write-Host "BUILD FAIL: $Name" -ForegroundColor Red
            $script:fail++
            $script:failures += "$Name (build)"
            return
        }
        Write-Host 'Building test binary...'
        $exe = 'het-test.exe'
        $testOut = & go test -c -o $exe . 2>&1
        if ($LASTEXITCODE -ne 0) {
            Write-Host $testOut
            Write-Host "TEST COMPILE FAIL: $Name" -ForegroundColor Red
            $script:fail++
            $script:failures += "$Name (test compile)"
            return
        }
        Write-Host 'Running tests...'
        & (Join-Path $Dir $exe) '--test.timeout=120s' 2>&1
        if ($LASTEXITCODE -ne 0) {
            Write-Host "TEST FAIL: $Name" -ForegroundColor Red
            $script:fail++
            $script:failures += "$Name (tests)"
        }
        else {
            Write-Host "PASS: $Name" -ForegroundColor Green
            $script:pass++
        }
        Remove-Item (Join-Path $Dir $exe) -ErrorAction SilentlyContinue
    }
    finally {
        Pop-Location
    }
}

function Test-GoPackage {
    param([string]$Name, [string]$Dir)
    Write-Host "`n=== $Name ===" -ForegroundColor Cyan
    Push-Location $Dir
    try {
        $exe = 'het-pkg-test.exe'
        $testOut = & go test -c -o $exe . 2>&1
        if ($LASTEXITCODE -ne 0) {
            Write-Host $testOut
            Write-Host "TEST COMPILE FAIL: $Name" -ForegroundColor Red
            $script:fail++
            $script:failures += "$Name (test compile)"
            return
        }
        & (Join-Path $Dir $exe) '--test.timeout=60s' 2>&1
        if ($LASTEXITCODE -ne 0) {
            Write-Host "TEST FAIL: $Name" -ForegroundColor Red
            $script:fail++
            $script:failures += "$Name (tests)"
        }
        else {
            Write-Host "PASS: $Name" -ForegroundColor Green
            $script:pass++
        }
        Remove-Item (Join-Path $Dir $exe) -ErrorAction SilentlyContinue
    }
    finally {
        Pop-Location
    }
}

# --- Go packages ---
Test-GoPackage 'shared/routing' (Join-Path $services 'shared\routing')
Test-GoPackage 'shared/noderec' (Join-Path $services 'shared\noderec')
Test-GoComponent 'nvpair-engine-manager' (Join-Path $services 'nvpair-engine-manager')
Test-GoComponent 'ollama-proxy' (Join-Path $services 'ollama-proxy')
Test-GoComponent 'lmstudio-proxy' (Join-Path $services 'lmstudio-proxy')
Test-GoComponent 'nvpair-node-scanner' (Join-Path $services 'nvpair-node-scanner')
Test-GoComponent 'nvpair-job-scheduler' (Join-Path $services 'nvpair-job-scheduler')

# --- Desktop ---
Write-Host "`n=== desktop (typecheck + heterogeneous tests) ===" -ForegroundColor Cyan
Push-Location $desktop
try {
    Write-Host 'Running typecheck...'
    npm run typecheck 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-Host 'TYPECHECK FAIL: desktop' -ForegroundColor Red
        $fail++
        $failures += 'desktop (typecheck)'
    }
    else {
        Write-Host 'PASS: desktop typecheck' -ForegroundColor Green
        $pass++
    }

    Write-Host 'Running heterogeneous desktop tests...'
    npm run test:unit -- --run 'tests/modular/engine-type-fallback.test.ts' 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-Host 'TEST FAIL: desktop heterogeneous' -ForegroundColor Red
        $fail++
        $failures += 'desktop (heterogeneous tests)'
    }
    else {
        Write-Host 'PASS: desktop heterogeneous tests' -ForegroundColor Green
        $pass++
    }
}
finally {
    Pop-Location
}

# --- Summary ---
Write-Host "`n" -NoNewline
Write-Host "========================================" -ForegroundColor Cyan
Write-Host " HETEROGENEOUS OFFLINE TEST SUMMARY     " -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host " Passed: $pass" -ForegroundColor $(if ($pass -gt 0) { 'Green' } else { 'Yellow' })
Write-Host " Failed: $fail" -ForegroundColor $(if ($fail -gt 0) { 'Red' } else { 'Green' })
if ($failures.Count -gt 0) {
    Write-Host " Failures:" -ForegroundColor Red
    foreach ($f in $failures) {
        Write-Host "   - $f" -ForegroundColor Red
    }
}
Write-Host "========================================" -ForegroundColor Cyan

if ($fail -gt 0) {
    exit 1
}
exit 0
