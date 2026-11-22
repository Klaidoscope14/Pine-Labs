#!/usr/bin/env bash
# Runs ON the instance (as ec2-user) after each deploy: secrets, images, stack, ledger, repo.
#   usage: aws-remote-setup.sh <region>
set -euo pipefail
region="$1"
cd /opt/aegis/src
export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH
while [[ ! -f /opt/aegis/.provisioned ]]; do echo "waiting for first-boot provisioning"; sleep 10; done
make -s secrets
secrets=deploy/compose/.env.secrets
if ! grep -q '^DEPLOY_API_KEY=' "$secrets"; then
  key="$(aws ssm get-parameter --region "$region" --name /aegis/deploy-api-key --with-decryption --query Parameter.Value --output text)"
  umask 077; printf 'DEPLOY_API_KEY=%s\n' "$key" >> "$secrets"; unset key
fi
# Shared secrets for the public API (CloudFront + Vercel) live in SSM; when present
# they replace the instance's locally generated values.
setsecret() {  # setsecret <env-name> <ssm-name>
  local v
  v="$(aws ssm get-parameter --region "$region" --name "$2" --with-decryption --query Parameter.Value --output text 2>/dev/null)" || return 0
  [[ -n "$v" && "$v" != None ]] || return 0
  umask 077
  grep -v "^$1=" "$secrets" > "$secrets.tmp" || true
  printf '%s=%s\n' "$1" "$v" >> "$secrets.tmp" && mv "$secrets.tmp" "$secrets"
}
setsecret AEGIS_BFF_TOKEN /aegis/bff-token
setsecret AEGIS_ORIGIN_SECRET /aegis/origin-secret
setsecret AEGIS_WEBAUTHN_ORIGIN /aegis/frontend-origin
docker info --format '{{json .Runtimes}}' | grep -q runsc || { echo "gVisor runtime missing"; exit 1; }
make up CONFIG=config.aws.yaml
make sandbox-image CONFIG=config.aws.yaml
make migrate CONFIG=config.aws.yaml
make seed-repo CONFIG=config.aws.yaml
echo "== gVisor check"
docker run --rm --runtime=runsc --network=none "$(bin/aegis-config sandbox-image config.aws.yaml)" python -c "import platform; print(platform.uname().release)"
echo "== services (worker + web app)"
make -s build CONFIG=config.aws.yaml
sudo install -m 644 deploy/aws/systemd/aegis-worker.service deploy/aws/systemd/aegis-api.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable aegis-worker aegis-api >/dev/null 2>&1
sudo systemctl restart aegis-worker aegis-api
sleep 3
for s in aegis-worker aegis-api; do printf '  %-14s %s\n' "$s" "$(systemctl is-active $s)"; done
curl -fsS -o /dev/null http://127.0.0.1:8480/healthz && echo "  web app answers on 127.0.0.1:8480"
echo "deploy complete"
