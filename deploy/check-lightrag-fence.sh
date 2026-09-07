#!/usr/bin/env bash
set -euo pipefail

fence_dir=${1:?usage: check-lightrag-fence.sh <fence-dir> <generation> <contract-sha256>}
generation=${2:?usage: check-lightrag-fence.sh <fence-dir> <generation> <contract-sha256>}
contract_sha256=${3:?usage: check-lightrag-fence.sh <fence-dir> <generation> <contract-sha256>}

[[ "$contract_sha256" =~ ^sha256:[0-9a-f]{64}$ ]] || {
  echo "invalid contract SHA-256" >&2
  exit 2
}

XLH_EXPECTED_CONTRACT_SHA256="$contract_sha256" \
python3 deploy/lightrag/rebuild_fence.py readiness \
  --fence-dir "$fence_dir" \
  --generation "$generation"
