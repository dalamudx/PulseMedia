#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# -lt 1 || $# -gt 2 || ! $1 =~ ^(base|usecase|runtime|http|postgres)$ ]];then echo 'usage: verify_m10_regression.sh base|usecase|runtime|http|postgres [part/total]' >&2;exit 2;fi
if [[ -n ${PULSE_M8_REGRESSION_TEST_NAMES:-} || -n ${PULSE_M8_REGRESSION_RESUME_DIR:-} ]];then echo 'Full M10 regression requires a fresh complete manifest, without follow-up or resume filters.' >&2;exit 2;fi
if [[ $# == 2 ]];then
  [[ $1 =~ ^(runtime|http|postgres)$ && $2 =~ ^[1-9][0-9]*/[1-9][0-9]*$ ]] || exit 2
  owned=$(mktemp -d /data/temp/pulse-m10-regression-part.XXXXXX)
  trap 'rc=$?;rm -rf -- "$owned";test ! -e "$owned" || exit 1;echo M10_PART_TEMP_CLEANUP_VERIFIED;exit "$rc"' EXIT
  export TMPDIR="$owned" TMP="$owned" TEMP="$owned"
  mise exec -- bash scripts/with_podman_test_postgres.sh node scripts/m10_regression_part.mjs "$1" "$2"
else
  printf 'M10_CURRENT_MANIFEST_RACE_START shard=%s reused_runner=M8_current_source old_results=NOT_REUSED\n' "$1"
  mise exec -- bash scripts/verify_m8_regression.sh "$1"
  printf 'M10_CURRENT_MANIFEST_RACE_PASS shard=%s owned_cleanup=VERIFIED gated_skips=SEPARATE milestone=NOT_RELEASED\n' "$1"
fi
