#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Deterministic, offline, unattended validation for the heterogeneous
# capability-aware routing work. Bash mirror of test-heterogeneous-offline.ps1.
#
# Guarantees (by construction): installs nothing; no network (GOPROXY=off);
# launches no engine, service binary, or full deployment; runs NO cross-process
# integration tests (never `go test ./...` at the services root, never
# services/tests, never `-tags live`) — only an explicit allowlist of pure/unit
# packages; requires no interaction.
#
# Exit 0 = every check that could run passed (absent toolchains are SKIPPED, not
# failures). Exit 1 = a check that ran FAILED.

set -uo pipefail

# Force offline behavior for child processes (process-scoped only).
export GOFLAGS='-mod=mod'
export GOPROXY='off'
export GOSUMDB='off'
export GOTOOLCHAIN='local'
export HF_HUB_OFFLINE=1
export TRANSFORMERS_OFFLINE=1
export npm_config_offline=true
export npm_config_audit=false
export npm_config_fund=false

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVICES="$REPO_ROOT/services"
DESKTOP="$REPO_ROOT/desktop"

FAILURES=0
RAN=0
SKIPPED=0

section() { echo; echo "==== $1 ===="; }
skip()    { echo "SKIPPED: $1"; SKIPPED=$((SKIPPED+1)); }
pass()    { echo "PASS: $1"; RAN=$((RAN+1)); }
fail()    { echo "FAIL: $1"; FAILURES=$((FAILURES+1)); RAN=$((RAN+1)); }
have()    { command -v "$1" >/dev/null 2>&1; }

go_test() { # <moduleDir> <label> <pkg...>
    local dir="$1"; local label="$2"; shift 2
    echo "> (cd $dir) go test $*"
    ( cd "$dir" && go test "$@" )
    if [ $? -eq 0 ]; then pass "$label"; else fail "$label (go test failed)"; fi
}

section "Toolchain detection"
HAS_GO=false;    have go    && HAS_GO=true
HAS_GOFMT=false; have gofmt && HAS_GOFMT=true
HAS_NODE=false;  have node  && HAS_NODE=true
HAS_NM=false;    [ -d "$DESKTOP/node_modules" ] && HAS_NM=true
echo "go=$HAS_GO gofmt=$HAS_GOFMT node=$HAS_NODE node_modules=$HAS_NM"

section "gofmt (routing package)"
if $HAS_GOFMT; then
    BAD="$(gofmt -l "$SERVICES/shared/routing" || true)"
    if [ -z "$BAD" ]; then pass "routing gofmt clean"; else fail "gofmt unformatted: $BAD"; fi
else
    skip "gofmt not on PATH"
fi

section "Go unit/component tests (allowlist)"
if $HAS_GO; then
    go_test "$SERVICES/shared" "shared routing/noderec/schedulerwire" ./routing/... ./noderec/... ./schedulerwire/...
    go_test "$SERVICES/nvpair-job-scheduler" "nvpair-job-scheduler" ./...
else
    skip "go not on PATH — Go tests not executed (documented offline limitation)"
fi

section "Go race detector (routing package)"
if $HAS_GO; then
    echo "> (cd $SERVICES/shared) go test -race ./routing/..."
    ( cd "$SERVICES/shared" && go test -race ./routing/... )
    rc=$?
    if [ $rc -eq 0 ]; then pass "routing -race"; else fail "routing -race (exit $rc; may need cgo/gcc)"; fi
else
    skip "go not on PATH — race tests not executed"
fi

section "Desktop typecheck + unit tests"
if $HAS_NODE && $HAS_NM; then
    ( cd "$DESKTOP" && npm run typecheck )
    if [ $? -eq 0 ]; then pass "desktop typecheck"; else fail "desktop typecheck"; fi
    ( cd "$DESKTOP" && npm run test:unit )
    if [ $? -eq 0 ]; then pass "desktop test:unit"; else fail "desktop test:unit"; fi
elif $HAS_NODE; then
    skip "desktop/node_modules absent — not installing (offline). Desktop tests not executed."
else
    skip "node not on PATH — desktop tests not executed"
fi

section "Summary"
echo "ran=$RAN failures=$FAILURES skipped=$SKIPPED"
if [ "$FAILURES" -gt 0 ]; then echo "RESULT: FAIL"; exit 1; fi
if [ "$RAN" -eq 0 ]; then echo "RESULT: NO CHECKS EXECUTED (toolchains unavailable). Nothing failed, nothing proven."; exit 0; fi
echo "RESULT: PASS"; exit 0
