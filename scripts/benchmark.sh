#!/usr/bin/env bash
# Model benchmark (PLAN.md §10.7): the same tickets through the same governed
# pipeline on each model, then a report from the ledger. SPENDS MONEY / QUOTA.
#   usage: scripts/benchmark.sh <runs-per-model> <label=config.yaml>...
#   e.g.   scripts/benchmark.sh 5 "Claude Sonnet 5.5=config.claude-code.yaml" "DeepSeek V4.1 Flash=config.deepseek.yaml"
set -uo pipefail
n="${1:?runs per model}"; shift
logs=.data/logs/bench; mkdir -p "$logs"
ids="$logs/runs-$(date +%Y%m%d-%H%M%S).tsv"
: > "$ids"
for pair in "$@"; do
  label="${pair%%=*}"; config="${pair#*=}"
  echo "== $label ($config): $n runs"
  bin/aegis-worker -config "$config" >>"$logs/worker-$(basename "$config" .yaml).log" 2>&1 &
  worker=$!
  sleep 4
  for i in $(seq 1 "$n"); do
    out="$(bin/aegis-run -config "$config" -scenario live-happy -timeout 40m 2>&1)"
    id="$(grep -oE 'live-happy-[0-9a-f]{8}' <<<"$out" | head -1)"
    status="$(awk '/live-happy-/{print $3; exit}' <<<"$out")"
    echo "   run $i: ${id:-?} ${status:-?}"
    [[ -n "$id" ]] && printf '%s\t%s\n' "$label" "$id" >> "$ids"
  done
  kill "$worker" 2>/dev/null; wait "$worker" 2>/dev/null
done
bin/aegis-bench -config "${BENCH_LEDGER_CONFIG:-config.local.yaml}" -runs "$ids" -out evals/benchmark.md
