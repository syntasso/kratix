#!/bin/bash
set -e

STOPPED_INSTANCES=$(aws ec2 describe-instances \
  --filters "Name=tag:runner,Values=true" "Name=instance-state-name,Values=stopped" \
  --query 'Reservations[*].Instances[*].InstanceId' \
  --output text)

if [ -z "$STOPPED_INSTANCES" ]; then
  exit 1
fi

INSTANCE_ARRAY=($STOPPED_INSTANCES)
INSTANCE_COUNT=${#INSTANCE_ARRAY[@]}
RANDOM_INDEX=$((RANDOM % INSTANCE_COUNT))
INSTANCE_ID=${INSTANCE_ARRAY[$RANDOM_INDEX]}

aws ec2 start-instances --instance-ids "$INSTANCE_ID" > /dev/null

aws ec2 wait instance-running --instance-ids "$INSTANCE_ID"

PUBLIC_HOSTNAME=$(aws ec2 describe-instances \
  --instance-ids "$INSTANCE_ID" \
  --query 'Reservations[0].Instances[0].PublicDnsName' \
  --output text)

echo $PUBLIC_HOSTNAME