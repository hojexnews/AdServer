#!/usr/bin/env bash
# Create the five AdServer topics. Partition counts mirror
# data/redpanda/topics.yaml; replication factor is 1 for single-node dev
# (production uses RF=3, see topics.yaml).
set -euo pipefail

B="redpanda:9092"
create() {
  echo "[rp-init] topic $1 (p=$2)"
  rpk topic create "$1" --partitions "$2" --replicas 1 --brokers "$B" || true
}

create adserver.ad-request.v1 16
create adserver.impression.v1 16
create adserver.click.v1       8
create adserver.conversion.v1  4
create adserver.decision.v1    16

rpk topic list --brokers "$B"
