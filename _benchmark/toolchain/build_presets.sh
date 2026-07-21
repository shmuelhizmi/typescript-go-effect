#!/usr/bin/env bash
# Build tsgo with several Go-toolchain "presets" and record build time + binary size.
#
# Presets are different ways of compiling the SAME tsgo source with the Go
# toolchain (microarchitecture level, PGO, bounds-check elision). The goal is
# to measure how much each buys on real TypeScript typecheck workloads.
#
# Usage:
#   PGO_PROFILE=/path/to/cpuprofile.pb.gz ./build_presets.sh
# Output binaries land in ./presets/tsgo-<name>.
set -uo pipefail

REPO=$(cd "$(dirname "$0")/../.." && pwd)   # typescript-go repo root
cd "$REPO"

OUT="$(dirname "$0")/presets"; OUT=$(cd "$(dirname "$0")" && pwd)/presets
mkdir -p "$OUT"
PGO_PROFILE="${PGO_PROFILE:-}"   # optional; PGO presets are skipped if unset

build() {
  local name="$1" env="$2" flags="$3"
  echo ">>> preset: $name  (env: ${env:-none}) (flags: ${flags:-none})"
  local start end rc secs size
  start=$(date +%s.%N)
  # shellcheck disable=SC2086
  env $env go build $flags -o "$OUT/tsgo-$name" ./cmd/tsgo
  rc=$?
  end=$(date +%s.%N)
  [ $rc -ne 0 ] && { echo "    FAILED (rc=$rc)"; return; }
  secs=$(awk "BEGIN{printf \"%.1f\", $end-$start}")
  size=$(du -h "$OUT/tsgo-$name" | cut -f1)
  echo "    ok  build=${secs}s  size=${size}"
}

build baseline ""            ""                          # default GOAMD64=v1
build v3       "GOAMD64=v3"  ""                          # AVX2
build v4       "GOAMD64=v4"  ""                          # AVX-512
build nobounds "GOAMD64=v3"  "-gcflags=all=-B"           # elide bounds checks
if [ -n "$PGO_PROFILE" ]; then
  build pgo "GOAMD64=v3" "-pgo=$PGO_PROFILE"
  build max "GOAMD64=v4" "-pgo=$PGO_PROFILE -gcflags=all=-B"
else
  echo ">>> PGO_PROFILE unset; skipping pgo/max presets"
fi

echo ">>> binaries:"; ls -la "$OUT"
