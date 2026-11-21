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
docker info --format '{{json .Runtimes}}' | grep -q runsc || { echo "gVisor runtime missing"; exit 1; }
make up CONFIG=config.aws.yaml
make sandbox-image CONFIG=config.aws.yaml
make migrate CONFIG=config.aws.yaml
make seed-repo CONFIG=config.aws.yaml
echo "== gVisor check"
docker run --rm --runtime=runsc --network=none "$(bin/aegis-config sandbox-image config.aws.yaml)" python -c "import platform; print(platform.uname().release)"
echo "deploy complete"
