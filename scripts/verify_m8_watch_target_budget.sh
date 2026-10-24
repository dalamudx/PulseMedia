#!/usr/bin/env bash
# Real wall-clock target resolution; race safety is covered by separate formal tasks.
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m8_watch_target_budget.sh' >&2; exit 2; fi
if [[ ${PULSE_M8_WATCH_TARGET_BUDGET_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M8_WATCH_TARGET_BUDGET_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m8_watch_target_budget.sh
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m8-watch-budget.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m8-watch-budget.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 98
 echo M8_WATCH_TARGET_BUDGET_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache" PULSE_M8_WATCH_WALL_CLOCK=1
mkdir -p "$GOTMPDIR"
log="$owned/targets.log"
if ! go test -count=1 -timeout=12m ./internal/store/postgres -run '^Test(AutomaticTV|MovieUpgrade)WatchIndependentTargetPaging65PG$' -v >"$log" 2>&1; then
 grep -E '^--- (FAIL|SKIP):|^[[:space:]]+[^ ]+_test.go:[0-9]+:|^FAIL|^# ' "$log" >&2 || true
 exit 1
fi
if grep -q -- '--- SKIP:' "$log"; then exit 1; fi
test "$(grep -c '^--- PASS: Test' "$log")" = 2
for family in automatic_tv upgrade; do
 test "$(grep -c "M8_WATCH_TARGET_WALL_CLOCK_PASS family=$family targets=65 pages=64+1 " "$log")" = 1
done
grep -E '^--- PASS: Test|M8_WATCH_TARGET_WALL_CLOCK_PASS' "$log"
printf 'M8_WATCH_TARGET_BUDGET_ACCEPTANCE_PASS parents=2 skipped=0 PG=18.6 wall_clock=REAL targets_each=65 pages_each=64_plus_1 query_resolution_only=true race=false predecessor_unchanged=true read_only=true full_dispatch_latency=NOT_MEASURED milestone=NOT_RELEASED\n'
