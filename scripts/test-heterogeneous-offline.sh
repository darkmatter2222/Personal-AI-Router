#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Deterministic, offline, unattended validation for the heterogeneous
# capability-aware routing work. Bash mirror of test-heterogeneous-offline.ps1.
#
# Usage:
#   test-heterogeneous-offline.sh                 # Developer mode (skips allowed)
#   test-heterogeneous-offline.sh --mode readiness # Readiness mode (skips FAIL)
#
# Guarantees in both modes: installs nothing (a missing toolchain FAILS rather
# than fetching it); no network (GOPROXY=off); launches no engine/service/
# deployment; runs NO cross-process integration tests (never `go test ./...` at
# the services root, never services/tests, never `-tags live`); no interaction.
# Readiness mode fails on a missing Go toolchain, any skipped mandatory check,
# an unavailable race detector, or routing coverage below the threshold.

set -uo pipefail

MODE="developer"
while [ $# -gt 0 ]; do
    case "$1" in
        --mode) MODE="$2"; shift 2 ;;
        --mode=*) MODE="${1#*=}"; shift ;;
        developer|readiness) MODE="$1"; shift ;;
        *) echo "unknown arg: $1" >&2; exit 2 ;;
    esac
done
READINESS=false; [ "$MODE" = "readiness" ] && READINESS=true

export GOFLAGS='-mod=mod' GOPROXY='off' GOSUMDB='off' GOTOOLCHAIN='local'
export HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1 npm_config_offline=true npm_config_audit=false npm_config_fund=false

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVICES="$REPO_ROOT/services"
DESKTOP="$REPO_ROOT/desktop"
ROUTING_COVERAGE_MIN="98.0"

FAILURES=0; RAN=0; SKIPPED=0

section() { echo; echo "==== $1 ($MODE mode) ===="; }
pass() { echo "PASS: $1"; RAN=$((RAN+1)); }
fail() { echo "FAIL: $1"; FAILURES=$((FAILURES+1)); RAN=$((RAN+1)); }
have() { command -v "$1" >/dev/null 2>&1; }
# skip_or_fail <msg> <mandatory:true|false>
skip_or_fail() {
    if $READINESS && [ "${2:-true}" = "true" ]; then fail "REQUIRED CHECK NOT EXECUTED: $1"; else echo "SKIPPED: $1"; SKIPPED=$((SKIPPED+1)); fi
}
go_test() { local dir="$1" label="$2"; shift 2; echo "> (cd $dir) go test $*"; ( cd "$dir" && go test "$@" ); if [ $? -eq 0 ]; then pass "$label"; else fail "$label"; fi; }

section "Toolchain detection"
HAS_GO=false; have go && HAS_GO=true
HAS_GOFMT=false; have gofmt && HAS_GOFMT=true
HAS_NODE=false; have node && HAS_NODE=true
HAS_NM=false; [ -d "$DESKTOP/node_modules" ] && HAS_NM=true
echo "go=$HAS_GO gofmt=$HAS_GOFMT node=$HAS_NODE node_modules=$HAS_NM"

if $READINESS && ! $HAS_GO; then
    echo; echo "REQUIRED TOOLCHAIN MISSING: Go is not on PATH. Readiness mode cannot"
    echo "install it (offline) and cannot pass without executing the Go suites."
    echo "RESULT: FAIL"; exit 1
fi

section "gofmt (routing package)"
if $HAS_GOFMT; then
    BAD="$(gofmt -l "$SERVICES/shared/routing" || true)"
    if [ -z "$BAD" ]; then pass "routing gofmt clean"; else fail "gofmt unformatted: $BAD"; fi
else
    skip_or_fail "gofmt not on PATH" true
fi

section "Go unit/component tests (allowlist)"
if $HAS_GO; then
    go_test "$SERVICES/shared" "shared routing/routeadapter/noderec/schedulerwire" ./routing/... ./routeadapter/... ./noderec/... ./schedulerwire/...
    go_test "$SERVICES/nvpair-job-scheduler" "nvpair-job-scheduler" ./...
else
    skip_or_fail "go not on PATH — Go unit tests not executed" true
fi

section "Go race detector (routing package)"
if $HAS_GO; then
    echo "> (cd $SERVICES/shared) go test -race ./routing/..."
    ( cd "$SERVICES/shared" && go test -race ./routing/... ); rc=$?
    if [ $rc -eq 0 ]; then pass "routing -race"; else fail "routing -race (exit $rc; needs a working cgo/C toolchain)"; fi
else
    skip_or_fail "go not on PATH — race tests not executed" true
fi

section "Go coverage (routing package, min ${ROUTING_COVERAGE_MIN}%)"
if $HAS_GO; then
    COVER="$(mktemp)"
    ( cd "$SERVICES/shared" && go test -coverprofile="$COVER" ./routing/... ); rc=$?
    if [ $rc -ne 0 ]; then
        fail "coverage run failed (exit $rc)"
    else
        TOTAL="$( cd "$SERVICES/shared" && go tool cover -func="$COVER" | awk '/total:/{print $NF}' | tr -d '%' )"
        echo "routing total coverage: ${TOTAL}%"
        if awk "BEGIN{exit !($TOTAL >= $ROUTING_COVERAGE_MIN)}"; then pass "routing coverage ${TOTAL}% >= ${ROUTING_COVERAGE_MIN}%"; else fail "routing coverage ${TOTAL}% < ${ROUTING_COVERAGE_MIN}%"; fi
    fi
    rm -f "$COVER"
else
    skip_or_fail "go not on PATH — coverage not measured" true
fi

section "Desktop typecheck + unit tests"
if $HAS_NODE && $HAS_NM; then
    ( cd "$DESKTOP" && npm run typecheck ); [ $? -eq 0 ] && pass "desktop typecheck" || fail "desktop typecheck"
    ( cd "$DESKTOP" && npm run test:unit ); [ $? -eq 0 ] && pass "desktop test:unit" || fail "desktop test:unit"
else
    DESKTOP_CHANGED=false
    if have git; then
        if ( cd "$REPO_ROOT" && git diff --name-only main...HEAD 2>/dev/null | grep -q '^desktop/' ); then DESKTOP_CHANGED=true; fi
    fi
    skip_or_fail "desktop/node_modules absent (not installing offline) — desktop tests not executed" "$DESKTOP_CHANGED"
fi

section "Summary"
echo "ran=$RAN failures=$FAILURES skipped=$SKIPPED"
if [ "$FAILURES" -gt 0 ]; then echo "RESULT: FAIL"; exit 1; fi
if $READINESS; then
    if [ "$SKIPPED" -gt 0 ]; then echo "RESULT: FAIL (readiness: $SKIPPED mandatory check(s) skipped)"; exit 1; fi
    echo "RESULT: PASS (readiness)"; exit 0
fi
if [ "$RAN" -eq 0 ]; then echo "RESULT: NO CHECKS EXECUTED (toolchains unavailable). Nothing failed, nothing proven."; exit 0; fi
echo "RESULT: PASS (developer)"; exit 0
