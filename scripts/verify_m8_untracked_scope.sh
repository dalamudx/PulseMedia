#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m8_untracked_scope.sh' >&2; exit 2; fi
if [[ $(uname -s) != Linux ]]; then echo 'Linux rootfs capabilities required' >&2; exit 2; fi
if [[ ${PULSE_M8_UNTRACKED_SCOPE_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M8_UNTRACKED_SCOPE_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m8_untracked_scope.sh
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m8-untracked-scope.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m8-untracked-scope.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M8_UNTRACKED_SCOPE_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache"
mkdir -p "$GOTMPDIR"
run_group(){
 local group=$1 package=$2 expected=$3 log="$owned/$1.log"
 if ! go test -race -count=1 -timeout=12m "$package" -run '^TestUntrackedScope|^TestM8UntrackedScopeConfig' -v >"$log" 2>&1; then
  echo "M8_UNTRACKED_SCOPE_${group}_FAILED" >&2
  grep -E '^--- (FAIL|SKIP):|^[[:space:]]+[^ ]+_test.go:[0-9]+:|^FAIL|^# ' "$log" >&2 || true
  exit 1
 fi
 if grep -q -- '--- SKIP:' "$log"; then echo "M8_UNTRACKED_SCOPE_${group}_UNEXPECTED_SKIP" >&2; exit 1; fi
 test "$(grep -c '^--- PASS: Test' "$log")" = "$expected"
 grep -E '^--- PASS: Test|M8_UNTRACKED_SCOPE_.*_PG_PASS' "$log"
}
run_group USECASE ./internal/usecase/library 14
run_group HTTP ./internal/httpapi 6
run_group STORE ./internal/store/postgres 14
run_group APP ./internal/app 3
printf 'M8_UNTRACKED_SCOPE_CONFIG_PASS parents=37 skipped=0 PG=18.6 app=REAL cookie=REAL rootfs=REAL configuration_only=true offline_disable=true historical_schema101=true persistent_review_boundary=true downloader_calls=0 execution_allowed=false discovery=NOT_IMPLEMENTED confirmation=NOT_IMPLEMENTED organization=NOT_IMPLEMENTED NAS=NOT_COVERED milestone=NOT_RELEASED\n'
