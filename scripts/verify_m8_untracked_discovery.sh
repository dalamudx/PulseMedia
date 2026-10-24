#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m8_untracked_discovery.sh' >&2; exit 2; fi
if [[ $(uname -s) != Linux ]]; then echo 'Linux inotify capabilities required' >&2; exit 2; fi
if [[ ${PULSE_M8_UNTRACKED_DISCOVERY_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M8_UNTRACKED_DISCOVERY_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m8_untracked_discovery.sh
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m8-untracked-discovery.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m8-untracked-discovery.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M8_UNTRACKED_DISCOVERY_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache" PULSE_M8_UNTRACKED_DISCOVERY_ACCEPTANCE=1
mkdir -p "$GOTMPDIR"
run_group(){
 local group=$1 package=$2 expected=$3 log="$owned/$1.log"
 if ! go test -race -count=1 -timeout=12m "$package" -run '^TestUntrackedDiscovery|^TestM8UntrackedDiscoveryRuntimeCookiePG$' -v >"$log" 2>&1; then
  echo "M8_UNTRACKED_DISCOVERY_${group}_FAILED" >&2
  grep -E '^--- (FAIL|SKIP):|^[[:space:]]+[^ ]+_test.go:[0-9]+:|^FAIL|^# ' "$log" >&2 || true
  exit 1
 fi
 if grep -q -- '--- SKIP:' "$log"; then echo "M8_UNTRACKED_DISCOVERY_${group}_UNEXPECTED_SKIP" >&2; exit 1; fi
 test "$(grep -c '^--- PASS: Test' "$log")" = "$expected"
 grep -E '^--- PASS: Test|M8_UNTRACKED_DISCOVERY_APP_PASS' "$log"
}
run_group USECASE ./internal/usecase/library 13
run_group HTTP ./internal/httpapi 4
run_group STORE ./internal/store/postgres 16
run_group APP ./internal/app 5
printf 'M8_UNTRACKED_DISCOVERY_ACCEPTANCE_PASS parents=38 skipped=0 PG=18.6 app=REAL cookie=REAL rootfs=REAL inotify=REAL periodic_60s_excluded=true observations_only=true GET_writes=0 downloader_calls=0 execution_allowed=false inventory_complete=false identification=NOT_IMPLEMENTED confirmation=NOT_IMPLEMENTED organization=NOT_IMPLEMENTED NAS=NOT_COVERED milestone=NOT_RELEASED\n'
