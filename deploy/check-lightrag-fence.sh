#!/usr/bin/env bash
set -euo pipefail

if (( $# != 4 )); then
  echo "usage: $0 <fence-dir> <generation> <contract-sha256> <shared-gid>" >&2
  exit 2
fi
fence_dir=$1
generation=$2
contract_sha256=$3
shared_gid=$4

[[ "$contract_sha256" =~ ^sha256:[0-9a-f]{64}$ ]] || {
  echo "invalid contract SHA-256" >&2
  exit 2
}
[[ "$shared_gid" =~ ^[1-9][0-9]{0,9}$ ]] && (( shared_gid <= 2147483647 )) || {
  echo "invalid shared GID" >&2
  exit 2
}

XLH_EXPECTED_CONTRACT_SHA256="$contract_sha256" \
python3 deploy/lightrag/rebuild_fence.py readiness \
  --fence-dir "$fence_dir" \
  --generation "$generation" \
  --expected-uid 1000 \
  --expected-gid "$shared_gid"
