#!/usr/bin/env bash
# RouteLatch T1 / unit A — one command that runs every check in this spike.
#
#   ./run.sh
#
# The exit cores run as child processes of this binary, so the measured process
# contains at most one embedded Xray instance. No service is installed and no
# public network exit is used.
set -euo pipefail
cd "$(dirname "$0")"

REPO_ROOT="$(cd ../.. && pwd)"
export PATH="$REPO_ROOT/.toolchain/go/bin:$PATH"
export GOTOOLCHAIN=local
export GOMODCACHE="$REPO_ROOT/.toolchain/modcache"
# proxy.golang.org serves .mod/.info directly but redirects .zip downloads to
# storage.googleapis.com, which is blocked in this environment. With a warm
# module cache the build needs no zip; on a cold cache use GOPROXY=direct
# (github.com is reachable) instead of bypassing the block.
export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"

mkdir -p results
BIN="${TMPDIR:-/tmp/opencode}/routelatch-t1-xray"
go build -o "$BIN" ./cmd/spike
"$BIN" all 2>&1 | tee results/last-run.txt
