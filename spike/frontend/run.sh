#!/usr/bin/env bash
# Reproduce every check in this spike with one command.
# Loopback sockets only; no services, credentials or external network.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"

export PATH="$root/.toolchain/go/bin:$PATH"
export GOTOOLCHAIN=local
export GOMODCACHE="$root/.toolchain/modcache"

cd "$here"
exec go test -count=1 -race -v ./... "$@"
