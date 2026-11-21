#!/usr/bin/env bash
# Deploys AegisSDLC to one EC2 instance (PLAN.md §10.4) and starts the stack.
#   usage: AWS_REGION=<region> [AWS_PROFILE=<profile>] [INSTANCE_TYPE=m7g.2xlarge] [SKIP_STACK=1] scripts/aws-deploy.sh
#   SKIP_STACK=1 ships code only (no CloudFormation update, so no instance stop/start).
# Needs: aws CLI v2 signed in. Nothing listens publicly; use scripts/aws-tunnel.sh to view UIs.
# The model API key must already be in SSM: see the "put the key" command printed below.
set -euo pipefail
: "${AWS_REGION:?set AWS_REGION to your India region}"
stack="${STACK:-aegis}"
itype="${INSTANCE_TYPE:-m7g.2xlarge}"
root="$(cd "$(dirname "$0")/.." && pwd)"
aws_() { aws --region "$AWS_REGION" ${AWS_PROFILE:+--profile "$AWS_PROFILE"} "$@"; }

if ! aws_ ssm get-parameter --name /aegis/deploy-api-key --query Parameter.Name --output text >/dev/null 2>&1; then
  echo "The DeepSeek key is not in SSM yet. Put it there (input is hidden, never echoed):"
  echo "  read -rs K && aws --region $AWS_REGION ssm put-parameter --name /aegis/deploy-api-key --type SecureString --value \"\$K\" --overwrite; unset K"
  exit 1
fi

if [[ "${SKIP_STACK:-0}" != 1 ]]; then
  echo "== stack $stack ($itype) in $AWS_REGION"
  aws_ cloudformation deploy --stack-name "$stack" --template-file "$root/deploy/aws/aegis.cfn.yaml" \
    --capabilities CAPABILITY_IAM --parameter-overrides "InstanceType=$itype" --no-fail-on-empty-changeset
fi
out() { aws_ cloudformation describe-stacks --stack-name "$stack" --query "Stacks[0].Outputs[?OutputKey=='$1'].OutputValue" --output text; }
instance="$(out InstanceId)"; bucket="$(out BundleBucket)"

echo "== bundling the working tree (tracked + untracked, never ignored files such as secrets)"
bundle="$(mktemp -t aegis-bundle).tgz"
(cd "$root" && git ls-files -co --exclude-standard -z | COPYFILE_DISABLE=1 tar --no-xattrs --no-mac-metadata --null -T - -czf "$bundle")
key="bundles/$(date +%Y%m%d-%H%M%S).tgz"
aws_ s3 cp "$bundle" "s3://$bucket/$key" --only-show-errors
rm -f "$bundle"

echo "== waiting for $instance to register with Systems Manager"
for _ in $(seq 1 60); do
  [[ "$(aws_ ssm describe-instance-information --filters "Key=InstanceIds,Values=$instance" --query 'length(InstanceInformationList)' --output text)" == 1 ]] && break
  sleep 10
done

echo "== installing and starting on the instance (takes several minutes the first time)"
cmd_id="$(aws_ ssm send-command --instance-ids "$instance" --document-name AWS-RunShellScript \
  --timeout-seconds 3600 --comment "aegis deploy $key" \
  --parameters "commands=[\"sudo -iu ec2-user bash -lc 'aws s3 cp s3://$bucket/$key /tmp/aegis.tgz --region $AWS_REGION && mkdir -p /opt/aegis/src && tar -xzf /tmp/aegis.tgz -C /opt/aegis/src && /opt/aegis/src/scripts/aws-remote-setup.sh $AWS_REGION'\"]" \
  --query Command.CommandId --output text)"
aws_ ssm wait command-executed --command-id "$cmd_id" --instance-id "$instance" || true
aws_ ssm get-command-invocation --command-id "$cmd_id" --instance-id "$instance" \
  --query '{status:Status,out:StandardOutputContent,err:StandardErrorContent}' --output json
echo "instance: $instance   shell: aws ssm start-session --target $instance --region $AWS_REGION"
