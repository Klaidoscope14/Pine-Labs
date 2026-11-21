#!/usr/bin/env bash
# Forwards one of the instance's loopback-only UIs to this laptop over Systems Manager.
#   usage: AWS_REGION=<region> scripts/aws-tunnel.sh <instance-id> <port>   (e.g. 3000 Grafana, 8233 Temporal UI, 16686 Jaeger)
set -euo pipefail
: "${AWS_REGION:?set AWS_REGION}"
aws --region "$AWS_REGION" ${AWS_PROFILE:+--profile "$AWS_PROFILE"} ssm start-session --target "$1" \
  --document-name AWS-StartPortForwardingSession --parameters "portNumber=$2,localPortNumber=$2"
