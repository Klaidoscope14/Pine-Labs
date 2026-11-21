#!/usr/bin/env bash
# Generates local-only random secrets. Never committed (see .gitignore).
set -euo pipefail
dir="$(cd "$(dirname "$0")/../deploy/compose" && pwd)"
umask 077
if [[ ! -f "$dir/.env.secrets" ]]; then
  {
    echo "POSTGRES_PASSWORD=$(openssl rand -hex 24)"
    echo "NATS_PASSWORD=$(openssl rand -hex 24)"
    echo "GRAFANA_ADMIN_PASSWORD=$(openssl rand -hex 16)"
  } > "$dir/.env.secrets"
  echo "created $dir/.env.secrets"
fi
if [[ ! -f "$dir/.s3.json" ]]; then
  cat > "$dir/.s3.json" <<JSON
{"identities":[{"name":"aegis","credentials":[{"accessKey":"$(openssl rand -hex 10)","secretKey":"$(openssl rand -hex 20)"}],"actions":["Admin","Read","Write","List","Tagging"]}]}
JSON
  chmod 644 "$dir/.s3.json"
  echo "created $dir/.s3.json"
fi
