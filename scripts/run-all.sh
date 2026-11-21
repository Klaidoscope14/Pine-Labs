#!/usr/bin/env bash
# Starts a worker, runs every scenario concurrently, then stops the worker.
set -euo pipefail
config="$1"
logdir="$2"
mkdir -p "$logdir"
bin/aegis-worker -config "$config" >>"$logdir/worker.log" 2>&1 &
worker=$!
trap 'kill "$worker" 2>/dev/null || true' EXIT
bin/aegis-run -config "$config" -scenario all
