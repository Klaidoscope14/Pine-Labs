#!/usr/bin/env bash
# Phase 2 offline proof: real agents on replayed model responses (no network, no cost).
# Runs live-happy end to end and checks the card number in the ticket never left the machine.
set -euo pipefail
logdir="$1"
mkdir -p "$logdir"
bin/aegis-worker -config config.replay.yaml >>"$logdir/worker-replay.log" 2>&1 &
worker=$!
trap 'kill "$worker" 2>/dev/null || true' EXIT
sleep 2
bin/aegis-run -config config.replay.yaml -scenario live-happy
sent=$(ls evals/cassettes/live-happy/*.sent.json | wc -l | tr -d ' ')
# grep exits 1 when nothing matches, which is the good outcome here.
leaked=$({ grep -l -E '4111 ?1111 ?1111 ?1111' evals/cassettes/live-happy/*.sent.json || true; } | wc -l | tr -d ' ')
redacted=$({ grep -l 'REDACTED:card_number' evals/cassettes/live-happy/*.sent.json || true; } | wc -l | tr -d ' ')
echo "outgoing requests: $sent, containing the card number: $leaked, with it redacted: $redacted"
[[ "$sent" -gt 0 && "$leaked" == 0 && "$redacted" -gt 0 ]] || { echo "FAIL: data filter check"; exit 1; }
echo "phase 2 offline check passed"
