#!/usr/bin/env bash
# Phase 0 exit check: every service in the local stack is healthy and egress is denied.
set -uo pipefail
fail=0
ok()  { printf '  \033[32mOK\033[0m   %s\n' "$1"; }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=1; }

echo "Container health (services with built-in health checks):"
for svc in postgres temporal nats seaweedfs prometheus grafana smokescreen; do
  state=$(docker inspect --format '{{.State.Health.Status}}' "aegis-$svc-1" 2>/dev/null || echo missing)
  [[ "$state" == healthy ]] && ok "$svc" || bad "$svc ($state)"
done

echo "HTTP checks from the host (127.0.0.1 only):"
check() { code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$2"); [[ "$code" == 200 ]] && ok "$1" || bad "$1 (HTTP $code)"; }
check otel-collector http://127.0.0.1:13133/
check jaeger         http://127.0.0.1:16686/
check temporal-ui    http://127.0.0.1:8233/

echo "Egress proxy must deny an unlisted host:"
code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 --proxy http://127.0.0.1:4750 http://example.com/)
[[ "$code" == 407 || "$code" == 403 ]] && ok "smokescreen denied example.com (HTTP $code)" || bad "smokescreen returned HTTP $code"

echo "Nothing listens on a non-loopback address:"
exposed=$(docker ps --filter label=com.docker.compose.project=aegis --format '{{.Ports}}' | tr ',' '\n' | grep -E '0\.0\.0\.0:|\[::\]:' || true)
[[ -z "$exposed" ]] && ok "all published ports bound to 127.0.0.1" || bad "exposed: $exposed"

exit $fail
