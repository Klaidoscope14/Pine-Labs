#!/usr/bin/env bash
# Phase 2 live proof with a real model. SPENDS MONEY (capped by the config's budget).
#   usage: [PARALLEL=1] phase2-live.sh <logdir> <runs> <config>
#   1. live-happy N times: reports the pass count (Phase 2 target: all N approved)
#   2. live-protected: the protected payout file must never be modified
#   3. kill switch: a live run is stopped and ends KILLED
set -uo pipefail
logdir="$1"
runs="${2:-5}"
config="${3:-config.bedrock.yaml}"
mkdir -p "$logdir"
tag="$(basename "$config" .yaml)"
bin/aegis-worker -config "$config" >>"$logdir/worker-$tag.log" 2>&1 &
worker=$!
trap 'kill "$worker" 2>/dev/null || true' EXIT
sleep 4

passed=0
if [[ "${PARALLEL:-0}" == 1 ]]; then
  # All runs at once: the platform's normal mode (separate worktrees, sandbox limit).
  echo "== live-happy: $runs runs in parallel ($tag)"
  pids=()
  for i in $(seq 1 "$runs"); do
    bin/aegis-run -config "$config" -scenario live-happy -timeout 40m >"$logdir/live-happy-$tag-$i.out" 2>&1 &
    pids+=($!)
  done
  for i in "${!pids[@]}"; do
    if wait "${pids[$i]}"; then passed=$((passed + 1)); fi
    cat "$logdir/live-happy-$tag-$((i + 1)).out"
  done
else
  for i in $(seq 1 "$runs"); do
    echo "== live-happy run $i of $runs ($tag)"
    if bin/aegis-run -config "$config" -scenario live-happy -timeout 40m; then
      passed=$((passed + 1))
    fi
  done
fi
echo "live-happy: $passed of $runs approved"

echo "== live-protected ($tag)"
protected_ok=0
bin/aegis-run -config "$config" -scenario live-protected -timeout 40m && protected_ok=1

echo "== kill switch ($tag)"
run_id="$(bin/aegis-run -config "$config" -scenario live-happy -detach)"
sleep 30
bin/aegis-ctl -config "$config" kill -tenant payments-team -reason "phase 2 kill-switch test"
sleep 15
status="$(bin/aegis-ledger -config "$config" show -tenant payments-team -run "$run_id" | python3 -c 'import json,sys; print(json.load(sys.stdin)["run"]["status"])')"
bin/aegis-ctl -config "$config" unkill -tenant payments-team -reason "phase 2 kill-switch test done"
echo "killed run $run_id ended: $status"

echo "== summary ($tag): live-happy $passed/$runs approved, protected file check $([ $protected_ok = 1 ] && echo passed || echo FAILED), kill switch $status"
[[ "$passed" == "$runs" && "$protected_ok" == 1 && "$status" == KILLED ]]
