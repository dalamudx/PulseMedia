#!/usr/bin/env bash
# Own server, real NFS client in a private mount namespace, exact cleanup.
set -euo pipefail
if [[ $# != 0 ]]; then echo 'Usage: verify_m8_nfs_watch.sh' >&2; exit 2; fi
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
export PULSE_M8_NFS_WATCH_ACCEPTANCE=1
exec bash scripts/m4_nfs_faults.sh
