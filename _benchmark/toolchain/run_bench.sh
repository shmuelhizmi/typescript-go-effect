#!/usr/bin/env bash
# Benchmark preset binaries (from build_presets.sh) against real TS workloads.
# No hyperfine dependency: warmup + N timed runs, report min/median/mean to CSV.
#
# Point these at your checkouts (all optional; a workload is skipped if unset):
#   TS_REPO      typescript-go repo root (has _submodules/TypeScript)   [auto]
#   VSCODE_DIR   microsoft/vscode checkout with node_modules installed
#   EFFECT_DIR   Effect-TS/effect checkout with node_modules installed
#   RUNS=5 WARMUP=1
set -uo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
OUT="$HERE/presets"
RESULTS="$HERE/results"; mkdir -p "$RESULTS"
CSV="$RESULTS/results.csv"
echo "workload,preset,env,min_s,median_s,mean_s,runs" > "$CSV"

RUNS=${RUNS:-5}; WARMUP=${WARMUP:-1}
TS_REPO="${TS_REPO:-$(cd "$HERE/../.." && pwd)}"
VSCODE_DIR="${VSCODE_DIR:-}"
EFFECT_DIR="${EFFECT_DIR:-}"

declare -A WL
WL[typescript]="$TS_REPO|-p _submodules/TypeScript/src/compiler --noEmit"
[ -n "$EFFECT_DIR" ] && WL[effect]="$EFFECT_DIR|-p packages/effect/tsconfig.json --noEmit"
[ -n "$VSCODE_DIR" ] && WL[vscode]="$VSCODE_DIR|-p src/tsconfig.json --noEmit"

median() { sort -n | awk '{a[NR]=$1} END{n=NR; if(n%2){print a[(n+1)/2]}else{printf "%.3f",(a[n/2]+a[n/2+1])/2}}'; }

time_one() {
  local bin="$1" dir="$2" args="$3" envstr="$4" s e
  s=$(date +%s.%N); ( cd "$dir" && env $envstr "$bin" $args >/dev/null 2>&1 ); e=$(date +%s.%N)
  awk "BEGIN{printf \"%.3f\", $e-$s}"
}

run_case() {
  local wl="$1" preset="$2" bin="$3" envlabel="$4" envstr="$5"
  local spec="${WL[$wl]:-}"; [ -z "$spec" ] && return
  local dir="${spec%%|*}" args="${spec##*|}"
  [ -x "$bin" ] || return
  local i t; for i in $(seq 1 "$WARMUP"); do time_one "$bin" "$dir" "$args" "$envstr" >/dev/null; done
  local times=(); for i in $(seq 1 "$RUNS"); do times+=("$(time_one "$bin" "$dir" "$args" "$envstr")"); done
  local min med mean
  min=$(printf "%s\n" "${times[@]}" | sort -n | head -1)
  med=$(printf "%s\n" "${times[@]}" | median)
  mean=$(printf "%s\n" "${times[@]}" | awk '{s+=$1}END{printf "%.3f",s/NR}')
  printf "  %-11s %-10s %-10s min=%-7s med=%-7s mean=%-7s\n" "$wl" "$preset" "$envlabel" "$min" "$med" "$mean"
  echo "$wl,$preset,$envlabel,$min,$med,$mean,$RUNS" >> "$CSV"
}

echo "=== build-time presets (default runtime env) ==="
for bin in "$OUT"/tsgo-*; do
  preset=$(basename "$bin" | sed 's/^tsgo-//')
  for wl in "${!WL[@]}"; do run_case "$wl" "$preset" "$bin" "default" ""; done
done

echo "=== runtime GC knobs (on fastest available binary) ==="
BEST="$OUT/tsgo-max"; [ -x "$BEST" ] || BEST="$OUT/tsgo-v3"; [ -x "$BEST" ] || BEST="$OUT/tsgo-baseline"
for wl in "${!WL[@]}"; do
  run_case "$wl" "$(basename "$BEST" | sed 's/tsgo-//')" "$BEST" "GOGC=off" "GOGC=off"
  run_case "$wl" "$(basename "$BEST" | sed 's/tsgo-//')" "$BEST" "GOGC=400" "GOGC=400"
done

echo "=== CSV: $CSV ==="; column -t -s, "$CSV"
