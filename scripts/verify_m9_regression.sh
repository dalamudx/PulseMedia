#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 1 || ! $1 =~ ^(base|usecase|runtime|http|postgres)$ ]];then echo 'usage: verify_m9_regression.sh base|usecase|runtime|http|postgres' >&2;exit 2;fi
if [[ -n ${PULSE_M8_REGRESSION_TEST_NAMES:-} || -n ${PULSE_M8_REGRESSION_RESUME_DIR:-} ]];then echo 'Full M9 regression requires a fresh complete manifest, without follow-up or resume filters.' >&2;exit 2;fi
printf 'M9_CURRENT_MANIFEST_RACE_START shard=%s reused_runner=M8_current_source old_results=NOT_REUSED\n' "$1"
mise exec -- bash scripts/verify_m8_regression.sh "$1"
printf 'M9_CURRENT_MANIFEST_RACE_PASS shard=%s owned_cleanup=VERIFIED gated_skips=SEPARATE milestone=NOT_RELEASED\n' "$1"
