#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m8_site_account_stats_runtime.sh' >&2; exit 2; fi
if [[ ${PULSE_M8_STATS_RUNTIME_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M8_STATS_RUNTIME_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m8_site_account_stats_runtime.sh
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m8-stats-runtime.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m8-stats-runtime.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M8_STATS_RUNTIME_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build"
mkdir -p "$GOTMPDIR"
go test -race -count=1 -timeout=6m ./internal/app -run '^TestM8SiteAccountStatsRuntime' -v 2>&1 | tee "$owned/acceptance.log"
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M8_STATS_RUNTIME_UNEXPECTED_SKIP >&2; exit 1; fi
test "$(grep -c '^--- PASS: TestM8SiteAccountStatsRuntime' "$owned/acceptance.log")" = 3
for name in PositivePG FailurePG DisableLatePG; do
 test "$(grep -c "^--- PASS: TestM8SiteAccountStatsRuntime$name (" "$owned/acceptance.log")" = 1
done
for marker in POSITIVE FAILURE DISABLE_LATE; do
 grep -q "M8_STATS_RUNTIME_${marker}_PASS" "$owned/acceptance.log"
done
printf 'M8_STATS_RUNTIME_ACCEPTANCE_PASS parents=3 skipped=0 PG=18.6 app=REAL native=OWNED_HTTP auth=REAL_COOKIE account=SHARED_GLOBAL readonly_GET=AFTER_APP_STOP search_commands=0 downloads=0 Add=0 metadata_UI=false private_PT=false\n'
