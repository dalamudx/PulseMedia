#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m8_complete_tv_watch.sh' >&2; exit 2; fi
if [[ $(uname -s) != Linux ]]; then echo 'Linux inotify required' >&2; exit 2; fi
if [[ ${PULSE_M8_COMPLETE_TV_WATCH_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M8_COMPLETE_TV_WATCH_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m8_complete_tv_watch.sh
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m8-complete-tv-watch.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m8-complete-tv-watch.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M8_COMPLETE_TV_WATCH_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build"
mkdir -p "$GOTMPDIR"
run_group(){
 local group=$1 package=$2 pattern=$3 expected=$4
 local log="$owned/$group.log"
 if ! go test -race -count=1 -timeout=8m "$package" -run "$pattern" -v >"$log" 2>&1; then
  echo "M8_COMPLETE_TV_WATCH_${group}_FAILED" >&2
  grep -E '^--- (FAIL|SKIP):|^[[:space:]]+[^ ]+_test.go:[0-9]+:|^FAIL|^# ' "$log" >&2 || true
  exit 1
 fi
 if grep -q -- '--- SKIP:' "$log"; then echo "M8_COMPLETE_TV_WATCH_${group}_UNEXPECTED_SKIP" >&2; exit 1; fi
 test "$(grep -c '^--- PASS: Test' "$log")" = "$expected"
 grep -E '^--- PASS: Test|M8_COMPLETE_TV_WATCH_APP_PASS' "$log"
}
run_group NATIVE ./internal/store/download '^TestCompleteTVHint' 5
run_group CONTROL ./internal/app '^TestCompleteTV(Watch|TrackingWake)' 4
run_group STORE ./internal/store/postgres '^TestCompleteTV(TrackingHint|Watch)' 18
run_group APP ./internal/httpapi '^TestCompleteTVTrackingRuntimeDirectoryHint$' 1
printf 'M8_COMPLETE_TV_WATCH_ACCEPTANCE_PASS parents=28 skipped=0 PG=18.6 app=REAL native=OWNED_HTTP inotify=REAL predecessor_unchanged=true independent_target_paging=65_64_plus_1_read_only before_30s=true organization=review_required automatic_TV=NOT_COVERED upgrade=NOT_COVERED untracked=NOT_COVERED NAS=NOT_COVERED milestone=NOT_RELEASED\n'
