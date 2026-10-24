#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# -lt 1 || $# -gt 2 || ! $1 =~ ^(base|usecase|runtime|http|postgres)$ ]];then echo 'usage: verify_m11_regression.sh base|usecase|runtime|http|postgres [part/total]' >&2;exit 2;fi
printf 'M11_CURRENT_MANIFEST_RACE_START shard=%s part=%s reused_runner=M10_current_source old_results=NOT_REUSED\n' "$1" "${2:-all}"
bash scripts/verify_m10_regression.sh "$@"
printf 'M11_CURRENT_MANIFEST_RACE_PASS shard=%s part=%s owned_cleanup=VERIFIED gated_skips=SEPARATE milestone=NOT_RELEASED\n' "$1" "${2:-all}"
