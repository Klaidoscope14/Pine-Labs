#!/usr/bin/env bash
# One entry point for operating AegisSDLC on AWS (PLAN.md §10.4). Uses the aws CLI.
#
#   scripts/aws.sh <command>
#
#   check     show the account, stack, instance state and whether the model key is in SSM
#   key       put the DeepSeek key from deploy/compose/.env.secrets into SSM (never printed)
#   deploy    ship this working tree to the instance and restart the stack (no instance restart)
#   stack     create/update the CloudFormation stack, then deploy (an update may stop/start the instance)
#   verify    run the Phase 2 live checks on the instance in the background (5 parallel runs)
#   result    show the live-check summary from the instance
#   logs      tail the instance's setup and live-check logs
#   shell     open a shell on the instance (Session Manager)
#   web       open the Aegis web app: tunnels the instance's 127.0.0.1:8480 to http://localhost:8480
#   public    give the API a public HTTPS address via CloudFront (for the Vercel frontend); prints AEGIS_API_URL
#   frontend  tell the API where the frontend lives (passkeys bind to it): scripts/aws.sh frontend https://<app>.vercel.app
#   vercel-env  print the values to paste into Vercel's environment variables (shows a secret on your screen)
#   services  show the worker and web app services on the instance (restart with: services restart)
#   tunnel    forward an instance UI to localhost: scripts/aws.sh tunnel 3000  (3000 Grafana, 8233 Temporal, 16686 Jaeger)
#   start     start the instance
#   stop      stop the instance (data stays on the disk; billing for compute stops)
#   drift     detect CloudFormation drift (console changes not in the template)
#
# Settings (environment): AWS_PROFILE (default aegis-bedrock), AWS_REGION (default ap-south-1),
# STACK (default aegis), INSTANCE_TYPE (default m7g.2xlarge), EXPECT_ACCOUNT (refuse other accounts).
set -euo pipefail
export AWS_PROFILE="${AWS_PROFILE:-aegis-bedrock}"
export AWS_REGION="${AWS_REGION:-ap-south-1}"
export AWS_PAGER=""
STACK="${STACK:-aegis}"
root="$(cd "$(dirname "$0")/.." && pwd)"
cmd="${1:-help}"

die() { echo "error: $*" >&2; exit 1; }
out() { aws cloudformation describe-stacks --stack-name "$STACK" --query "Stacks[0].Outputs[?OutputKey=='$1'].OutputValue" --output text 2>/dev/null; }
instance() { local id; id="$(out InstanceId)"; [[ -n "$id" && "$id" != None ]] || die "stack $STACK has no instance (run: scripts/aws.sh stack)"; echo "$id"; }

account_guard() {
  local acct; acct="$(aws sts get-caller-identity --query Account --output text)" || die "not signed in (aws login --profile $AWS_PROFILE)"
  if [[ -n "${EXPECT_ACCOUNT:-}" && "$acct" != "$EXPECT_ACCOUNT" ]]; then
    die "signed in to account $acct, expected $EXPECT_ACCOUNT"
  fi
  echo "$acct"
}

# run_remote "<shell commands>": runs as ec2-user on the instance, waits, prints output.
# The script travels base64-encoded and is piped into a login shell, so no quoting
# layer (local shell, SSM, sudo -i) can mangle it.
run_remote() {
  local id cid status
  id="$(instance)"
  cid="$(aws ssm send-command --instance-ids "$id" --document-name AWS-RunShellScript \
    --timeout-seconds 3600 --parameters "$(jq -cn --arg c "echo $(printf '%s' "$1" | base64 | tr -d '\n') | base64 -d > /tmp/aegis-cmd.sh && chmod 644 /tmp/aegis-cmd.sh && sudo -iu ec2-user bash -l /tmp/aegis-cmd.sh; rc=\$?; rm -f /tmp/aegis-cmd.sh; exit \$rc" '{commands:[$c], executionTimeout:["3600"]}')" \
    --query Command.CommandId --output text)"
  for _ in $(seq 1 360); do
    status="$(aws ssm get-command-invocation --command-id "$cid" --instance-id "$id" --query Status --output text 2>/dev/null || echo Pending)"
    case "$status" in Pending|InProgress|Delayed) sleep 5 ;; *) break ;; esac
  done
  aws ssm get-command-invocation --command-id "$cid" --instance-id "$id" --query StandardOutputContent --output text
  local err; err="$(aws ssm get-command-invocation --command-id "$cid" --instance-id "$id" --query StandardErrorContent --output text)"
  [[ -z "$err" || "$err" == None ]] || echo "$err" | grep -v "Ignoring unknown extended header" >&2 || true
  [[ "$status" == Success ]] || die "remote command ended: $status"
}

command -v aws >/dev/null || die "aws CLI not found"
command -v jq >/dev/null || die "jq not found"

case "$cmd" in
  check)
    echo "account:  $(account_guard)   region: $AWS_REGION   profile: $AWS_PROFILE"
    if aws cloudformation describe-stacks --stack-name "$STACK" >/dev/null 2>&1; then
      id="$(instance)"
      echo "stack:    $STACK ($(aws cloudformation describe-stacks --stack-name "$STACK" --query 'Stacks[0].StackStatus' --output text))"
      echo "instance: $id $(aws ec2 describe-instances --instance-ids "$id" --query 'Reservations[0].Instances[0].[InstanceType,State.Name]' --output text)"
      echo "ssm:      $(aws ssm describe-instance-information --filters "Key=InstanceIds,Values=$id" --query 'InstanceInformationList[0].PingStatus' --output text)"
    else
      echo "stack:    $STACK does not exist yet (scripts/aws.sh stack)"
    fi
    if aws ssm get-parameter --name /aegis/deploy-api-key --query Parameter.Name --output text >/dev/null 2>&1; then
      echo "key:      /aegis/deploy-api-key present"
    else
      echo "key:      missing (scripts/aws.sh key)"
    fi
    ;;

  key)
    account_guard >/dev/null
    secrets="$root/deploy/compose/.env.secrets"
    grep -q '^DEPLOY_API_KEY=' "$secrets" 2>/dev/null || die "DEPLOY_API_KEY not in $secrets"
    # Piped via stdin so the key never appears on a command line or in output.
    grep '^DEPLOY_API_KEY=' "$secrets" | cut -d= -f2- | tr -d '\n' |
      aws ssm put-parameter --name /aegis/deploy-api-key --type SecureString --value file:///dev/stdin --overwrite --query Version --output text |
      sed 's/^/stored \/aegis\/deploy-api-key, version /'
    ;;

  deploy)
    account_guard >/dev/null
    SKIP_STACK=1 "$root/scripts/aws-deploy.sh"
    ;;

  stack)
    account_guard >/dev/null
    "$root/scripts/aws-deploy.sh"
    ;;

  verify)
    run_remote "sudo systemctl reset-failed aegis-live >/dev/null 2>&1 || true
      if systemctl is-active --quiet aegis-live; then echo 'a live check is already running'; exit 0; fi
      sudo systemd-run --unit=aegis-live --uid=ec2-user --gid=ec2-user --setenv=HOME=/home/ec2-user \
        bash -c 'cd /opt/aegis/src && export PATH=/usr/local/go/bin:\$PATH && date -u +%FT%TZ > /opt/aegis/live.start && make phase2-live LIVE_CONFIG=config.aws.yaml RUNS=5 PARALLEL=1 > /opt/aegis/live.log 2>&1; echo exit=\$? >> /opt/aegis/live.log'
      echo 'live check started (about 10-15 minutes); see: scripts/aws.sh result'"
    ;;

  result)
    run_remote "echo \"running: \$(systemctl is-active aegis-live)  started: \$(cat /opt/aegis/live.start 2>/dev/null)\"
      grep -E 'live-(happy|protected)-[0-9a-f]{8} |of [0-9]+ approved|killed run|summary|exit=' /opt/aegis/live.log 2>/dev/null | cut -c1-200 || echo 'no live check has run yet'"
    ;;

  logs)
    run_remote "echo '== setup'; tail -15 /opt/aegis/setup.log 2>/dev/null; echo '== live'; tail -25 /opt/aegis/live.log 2>/dev/null"
    ;;

  shell)
    aws ssm start-session --target "$(instance)"
    ;;

  web)
    echo "Aegis web app: http://localhost:8480  (Ctrl-C to close the tunnel)"
    ( sleep 4; command -v open >/dev/null && open http://localhost:8480 ) &
    aws ssm start-session --target "$(instance)" --document-name AWS-StartPortForwardingSession \
      --parameters "portNumber=8480,localPortNumber=8480"
    ;;

  services)
    if [[ "${2:-}" == restart ]]; then
      run_remote "sudo systemctl restart aegis-worker aegis-api; sleep 2; systemctl --no-pager --lines=0 status aegis-worker aegis-api | grep -E 'Loaded|Active|aegis-'"
    else
      run_remote "systemctl --no-pager --lines=0 status aegis-worker aegis-api | grep -E '●|Active'; curl -fsS http://127.0.0.1:8480/healthz && echo"
    fi
    ;;

  public)
    account_guard >/dev/null
    # Shared secrets: generated once, kept in SSM (SecureString), never printed here.
    for name in bff-token origin-secret; do
      if ! aws ssm get-parameter --name "/aegis/$name" --query Parameter.Name --output text >/dev/null 2>&1; then
        openssl rand -hex 32 | tr -d '\n' | aws ssm put-parameter --name "/aegis/$name" --type SecureString --value file:///dev/stdin >/dev/null
        echo "created /aegis/$name"
      fi
    done
    pl="$(aws ec2 describe-managed-prefix-lists --filters Name=prefix-list-name,Values=com.amazonaws.global.cloudfront.origin-facing \
      --query 'PrefixLists[0].PrefixListId' --output text)"
    [[ "$pl" == pl-* ]] || die "CloudFront prefix list not found in $AWS_REGION"
    origin="$(aws ssm get-parameter --name /aegis/origin-secret --with-decryption --query Parameter.Value --output text)"
    echo "== updating stack $STACK (CloudFront + Elastic IP + firewall rule); if the instance template changed, it restarts once"
    aws cloudformation deploy --stack-name "$STACK" --template-file "$root/deploy/aws/aegis.cfn.yaml" --capabilities CAPABILITY_IAM \
      --parameter-overrides "CloudFrontPrefixListId=$pl" "OriginSecret=$origin" --no-fail-on-empty-changeset
    unset origin
    echo "== shipping code so the API picks up the shared secrets"
    SKIP_STACK=1 "$root/scripts/aws-deploy.sh"
    echo
    echo "AEGIS_API_URL=$(out ApiUrl)"
    echo "Next: scripts/aws.sh vercel-env   (values for Vercel)"
    ;;

  frontend)
    origin="${2:-}"
    [[ "$origin" =~ ^https://[a-zA-Z0-9.-]+$ ]] || die "usage: scripts/aws.sh frontend https://<app>.vercel.app"
    aws ssm put-parameter --name /aegis/frontend-origin --type String --value "$origin" --overwrite >/dev/null
    echo "passkeys will bind to $origin; restarting the API with it"
    run_remote "cd /opt/aegis/src && v=\$(aws ssm get-parameter --region $AWS_REGION --name /aegis/frontend-origin --query Parameter.Value --output text) &&
      grep -v '^AEGIS_WEBAUTHN_ORIGIN=' deploy/compose/.env.secrets > /tmp/s && echo \"AEGIS_WEBAUTHN_ORIGIN=\$v\" >> /tmp/s && install -m 600 /tmp/s deploy/compose/.env.secrets && rm -f /tmp/s &&
      sudo systemctl restart aegis-api && sleep 2 && systemctl is-active aegis-api"
    ;;

  vercel-env)
    url="$(out ApiUrl)"; [[ -n "$url" && "$url" != None ]] || die "no public API yet (scripts/aws.sh public)"
    echo "# Paste into Vercel → Project → Settings → Environment Variables (Production)."
    echo "AEGIS_API_URL=$url"
    echo "AEGIS_BFF_TOKEN=$(aws ssm get-parameter --name /aegis/bff-token --with-decryption --query Parameter.Value --output text)"
    echo "AEGIS_SESSION_SECRET=$(openssl rand -hex 32)"
    echo "AEGIS_ACCESS_CODE=<choose a long access code and share it only with your reviewers>"
    ;;

  tunnel)
    port="${2:-3000}"
    echo "forwarding localhost:$port -> instance:$port (Ctrl-C to stop)"
    aws ssm start-session --target "$(instance)" --document-name AWS-StartPortForwardingSession \
      --parameters "portNumber=$port,localPortNumber=$port"
    ;;

  start)
    id="$(instance)"; aws ec2 start-instances --instance-ids "$id" >/dev/null
    aws ec2 wait instance-running --instance-ids "$id"; echo "started $id"
    ;;

  stop)
    id="$(instance)"; aws ec2 stop-instances --instance-ids "$id" >/dev/null
    aws ec2 wait instance-stopped --instance-ids "$id"; echo "stopped $id (disk kept)"
    ;;

  drift)
    did="$(aws cloudformation detect-stack-drift --stack-name "$STACK" --query StackDriftDetectionId --output text)"
    for _ in $(seq 1 60); do
      st="$(aws cloudformation describe-stack-drift-detection-status --stack-drift-detection-id "$did" --query DetectionStatus --output text)"
      [[ "$st" == DETECTION_IN_PROGRESS ]] || break; sleep 3
    done
    aws cloudformation describe-stack-drift-detection-status --stack-drift-detection-id "$did" --query '[StackDriftStatus,DriftedStackResourceCount]' --output text
    aws cloudformation describe-stack-resource-drifts --stack-name "$STACK" --stack-resource-drift-status-filters MODIFIED DELETED \
      --query 'StackResourceDrifts[].[LogicalResourceId,StackResourceDriftStatus]' --output text
    ;;

  help|-h|--help)
    sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
    ;;

  *)
    die "unknown command '$cmd' (scripts/aws.sh help)"
    ;;
esac
