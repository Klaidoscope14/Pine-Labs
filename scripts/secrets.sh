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
    echo "AEGIS_APP_DB_PASSWORD=$(openssl rand -hex 24)"
  } > "$dir/.env.secrets"
  echo "created $dir/.env.secrets"
fi
# Add any key introduced after the file was first created, without touching existing values.
for key in AEGIS_APP_DB_PASSWORD; do
  if ! grep -q "^$key=" "$dir/.env.secrets"; then
    echo "$key=$(openssl rand -hex 24)" >> "$dir/.env.secrets"
    echo "added $key to $dir/.env.secrets"
  fi
done
if [[ ! -f "$dir/.s3.json" ]]; then
  cat > "$dir/.s3.json" <<JSON
{"identities":[{"name":"aegis","credentials":[{"accessKey":"$(openssl rand -hex 10)","secretKey":"$(openssl rand -hex 20)"}],"actions":["Admin","Read","Write","List","Tagging"]}]}
JSON
  chmod 644 "$dir/.s3.json"
  echo "created $dir/.s3.json"
fi
