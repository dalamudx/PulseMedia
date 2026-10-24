#!/usr/bin/env bash
set -euo pipefail
umask 077
cd "$(dirname -- "$0")/.."
if [[ $# != 1 || ! $1 =~ ^(base|usecase|runtime|http|postgres)$ ]]; then
  echo 'Usage: verify_m8_regression.sh base|usecase|runtime|http|postgres' >&2
  exit 2
fi
if [[ ${PULSE_M8_REGRESSION_OWNED_PG:-} != 1 ]]; then
  exec env PULSE_M8_REGRESSION_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m8_regression.sh "$1"
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
if [[ -n ${PULSE_M8_REGRESSION_RESUME_DIR:-} ]]; then
  owned=$PULSE_M8_REGRESSION_RESUME_DIR
  [[ $owned =~ ^/data/temp/pulse-m8-regression\.[A-Za-z0-9]+$ ]] && test -d "$owned" && test ! -L "$owned"
  test -f "$owned/SHARD" && test ! -L "$owned/SHARD" && [[ $(cat "$owned/SHARD") == "$1" ]]
else
  owned=$(mktemp -d /data/temp/pulse-m8-regression.XXXXXX)
  printf '%s\n' "$1" > "$owned/SHARD"
fi
cleanup(){
  rc=$?; trap - EXIT
  case "$owned" in /data/temp/pulse-m8-regression.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
  test ! -e "$owned" || exit 1
  printf 'M8_REGRESSION_OWNED_TEMP_CLEANUP_VERIFIED shard=%s\n' "$1"
  exit "$rc"
}
trap 'cleanup "$1"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/cache"
mkdir -p "$GOTMPDIR"
node scripts/m8_regression.mjs "$1" "$owned"
