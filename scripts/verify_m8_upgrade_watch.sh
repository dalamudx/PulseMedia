#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m8_upgrade_watch.sh' >&2; exit 2; fi
if [[ $(uname -s) != Linux ]]; then echo 'Linux inotify required' >&2; exit 2; fi
if [[ ${PULSE_M8_UPGRADE_WATCH_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M8_UPGRADE_WATCH_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m8_upgrade_watch.sh
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m8-upgrade-watch.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m8-upgrade-watch.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M8_UPGRADE_WATCH_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache"
export PULSE_M8_MOVIE_UPGRADE_WATCH_ACCEPTANCE=1
mkdir -p "$GOTMPDIR"
run_group(){
 local group=$1 package=$2 pattern=$3 expected=$4
 local log="$owned/$group.log"
 if ! go test -race -count=1 -timeout=12m "$package" -run "$pattern" -v >"$log" 2>&1; then
  echo "M8_UPGRADE_WATCH_${group}_FAILED" >&2
  grep -E '^--- (FAIL|SKIP):|^[[:space:]]+[^ ]+_test.go:[0-9]+:|^FAIL|^# ' "$log" >&2 || true
  exit 1
 fi
 if grep -q -- '--- SKIP:' "$log"; then echo "M8_UPGRADE_WATCH_${group}_UNEXPECTED_SKIP" >&2; exit 1; fi
 test "$(grep -c '^--- PASS: Test' "$log")" = "$expected"
 grep -E '^--- PASS: Test|M8_MOVIE_UPGRADE_WATCH_(APP|UNKNOWN)_PASS|M8_UPGRADE_TARGET_PAGING_PASS' "$log"
}
run_group USECASE ./internal/usecase/download '^TestMovieUpgradeHint' 5
run_group CONTROL ./internal/app '^TestMovieUpgradeTracking(WakeOnlyClaims|WorkerPayload|WorkerDrainsActualReturn)$' 3
run_group STORE ./internal/store/postgres '^TestMovieUpgradeWatch' 9
test "$(grep -c 'M8_UPGRADE_TARGET_PAGING_PASS targets=65 pages=64+1' "$owned/STORE.log")" = 1
run_group APP ./internal/httpapi '^TestM8MovieUpgradeDirectoryHintHTTPPG$' 1
test "$(grep -c 'M8_MOVIE_UPGRADE_WATCH_APP_PASS scenario=' "$owned/APP.log")" = 2
test "$(grep -c 'M8_MOVIE_UPGRADE_WATCH_UNKNOWN_PASS scenario=' "$owned/APP.log")" = 2
printf 'M8_UPGRADE_WATCH_ACCEPTANCE_PASS parents=18 skipped=0 PG=18.6 app=REAL native=OWNED_HTTP inotify=REAL gate_30s_preserved=true periodic_excluded=true source_bindings=65 Add=1 organization=review_required ack_stop_at_cutoff=true ack_replace_if_better=true lost_ack_unknown_protection=2_scenarios original_receipt=true unknown_grants_no_tracking=true target_tasks_65=STRUCTURE_CLOCK_INJECTED target_pages=64_plus_1 native_Add_each=1 native_partial=true predecessor_frozen=true read_only=true wall_clock_65_performance=NOT_MEASURED untracked=NOT_COVERED NAS=NOT_COVERED milestone=NOT_RELEASED\n'
