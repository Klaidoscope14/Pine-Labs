#!/usr/bin/env bash
# Phase 1 exit criterion 5: kill the worker mid-run, restart it, and prove the run
# completes with no duplicate commits or ledger entries.
# Expects secrets in the environment and no other worker running.
set -euo pipefail
config="$1"
logdir="$2"
mkdir -p "$logdir"

start_worker() {
  bin/aegis-worker -config "$config" >>"$logdir/worker.log" 2>&1 &
  echo $!
}

pid="$(start_worker)"
run_id="$(bin/aegis-run -config "$config" -scenario happy -detach)"
echo "started run $run_id (worker pid $pid)"

# Wait until the run is in the gates, then kill the worker hard.
for _ in $(seq 1 120); do
  status="$(bin/aegis-ledger -config "$config" show -tenant payments-team -run "$run_id" 2>/dev/null \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["run"]["status"])' 2>/dev/null || true)"
  if [[ "$status" == GATES_* ]]; then break; fi
  sleep 1
done
echo "run is in $status: killing worker $pid with SIGKILL"
kill -9 "$pid"
wait "$pid" 2>/dev/null || true
sleep 3

pid="$(start_worker)"
echo "restarted worker (pid $pid)"
trap 'kill "$pid" 2>/dev/null || true' EXIT

bin/aegis-run -config "$config" -scenario happy -follow "$run_id"

summary="$(bin/aegis-ledger -config "$config" show -tenant payments-team -run "$run_id")"
python3 - "$summary" <<'PY'
import json, sys
s = json.loads(sys.argv[1])
seqs = [st["seq"] for st in s["steps"]]
assert seqs == list(range(1, len(seqs) + 1)), f"steps not gap-free/duplicate-free: {seqs}"
develops = [st for st in s["steps"] if st["node"] == "develop"]
assert len(develops) == 1, f"develop recorded {len(develops)} times"
print(f"ledger: {len(seqs)} steps, gap-free, one develop step")
PY

mirror_branch="agent/run-$run_id"
remote="$(bin/aegis-config repo-remote "$config")"
mirror="$(ls -d .data/mirrors/*.git | head -1)"
for m in .data/mirrors/*.git; do
  if git -C "$m" rev-parse --verify --quiet "$mirror_branch" >/dev/null; then mirror="$m"; fi
done
base="$(bin/aegis-ledger -config "$config" show -tenant payments-team -run "$run_id" | python3 -c 'import json,sys; print(json.load(sys.stdin)["run"]["base_sha"])')"
commits="$(git -C "$mirror" rev-list --count "$base..$mirror_branch")"
keyed="$(git -C "$mirror" log --format=%B "$base..$mirror_branch" | grep -c "Aegis-Key: $run_id:develop:1" || true)"
echo "branch $mirror_branch: $commits commit(s), $keyed with the attempt-1 key"
[[ "$commits" == 1 && "$keyed" == 1 ]] || { echo "FAIL: duplicate or missing commits"; exit 1; }
echo "crash test passed"
