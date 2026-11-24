#!/usr/bin/env bash
# Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
# Author: Albert <albert@rocareer.com>
#
# Public CI command set. The workflow calls one stage per step; the release
# rehearsal replays those same `run:` lines from ci.yml on the export tree.
# `all` is the local shorthand for every stage in order.
#
# Usage:
#   bash .github/ci/ci.sh fmt
#   bash .github/ci/ci.sh build
#   bash .github/ci/ci.sh test
#   bash .github/ci/ci.sh race
#   bash .github/ci/ci.sh admin
#   bash .github/ci/ci.sh cross
#   bash .github/ci/ci.sh all
#
# Tests run with -short: the scale and profiling fixtures (multi-million-posting
# synthetic corpora) are measurement instruments, not correctness gates, and
# are far too slow under -race on shared runners. The explicit -timeout values
# keep one slow package away from the go test default ten-minute cliff.
set -eo pipefail

stage="${1:-all}"

run_fmt() {
    local unformatted
    unformatted="$(gofmt -l cmd internal sdk diagnostics)" || {
        echo "gofmt could not read cmd/ internal/ sdk/ diagnostics/" >&2
        return 1
    }
    if [ -n "$unformatted" ]; then
        echo "unformatted files:" >&2
        printf '%s\n' "$unformatted" >&2
        return 1
    fi
}

run_build() {
    go build ./...
    go vet ./...
}

run_test() {
    go test -short -timeout=15m ./... -count=1
}

run_race() {
    go test -short -race -timeout=20m ./internal/... -count=1
}

run_admin() {
    if [ -d admin ]; then
        ( cd admin && go build ./... && go vet ./internal/rmconsole/... && \
          go test -short -timeout=10m ./internal/rmconsole/... -count=1 )
    fi
    if [ -d admin/web ]; then
        ( cd admin/web && npm ci --no-audit --no-fund && npm run build )
    fi
}

run_cross() {
    local target
    for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64; do
        CGO_ENABLED=0 GOOS="${target%/*}" GOARCH="${target#*/}" \
            go build -trimpath -o /dev/null ./cmd/rolling-memory
    done
}

run_stage() {
    case "$1" in
        fmt) run_fmt ;;
        build) run_build ;;
        test) run_test ;;
        race) run_race ;;
        admin) run_admin ;;
        cross) run_cross ;;
        *) echo "unknown stage: $1" >&2; return 2 ;;
    esac
}

case "$stage" in
    all)
        for s in fmt build test race admin cross; do
            printf 'ci: %s\n' "$s"
            run_stage "$s"
        done
        ;;
    fmt|build|test|race|admin|cross)
        run_stage "$stage"
        ;;
    *)
        echo "usage: $0 [fmt|build|test|race|admin|cross|all]" >&2
        exit 2
        ;;
esac
