#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m8_site_checkin_runtime.sh' >&2; exit 2; fi
if [[ ${PULSE_M8_CHECKIN_RUNTIME_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M8_CHECKIN_RUNTIME_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m8_site_checkin_runtime.sh
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m8-checkin-runtime.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m8-checkin-runtime.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M8_CHECKIN_RUNTIME_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build"
mkdir -p "$GOTMPDIR"
go test -race -count=1 -timeout=6m ./internal/app -run '^TestM8SiteCheckinRuntime(PositivePG|LostReceiptPG|PreparedRestartPG|DisableLatePG)$' -v >"$owned/acceptance.log" 2>&1 || { echo M8_CHECKIN_RUNTIME_GO_FAILED >&2; grep -E '^--- (FAIL|SKIP):|^[[:space:]]+[^ ]+_test.go:[0-9]+:|^FAIL|^# ' "$owned/acceptance.log" >&2 || true; exit 1; }
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M8_CHECKIN_RUNTIME_UNEXPECTED_SKIP >&2; exit 1; fi
test "$(grep -c '^--- PASS: TestM8SiteCheckinRuntime' "$owned/acceptance.log")" = 4
for name in PositivePG LostReceiptPG PreparedRestartPG DisableLatePG; do
 test "$(grep -c "^--- PASS: TestM8SiteCheckinRuntime$name (" "$owned/acceptance.log")" = 1
done
for marker in POSITIVE LOST_RECEIPT PREPARED_RESTART DISABLE_LATE; do
 test "$(grep -c "M8_CHECKIN_RUNTIME_${marker}_PASS" "$owned/acceptance.log")" = 1
done
grep -E '^--- PASS: TestM8SiteCheckinRuntime|M8_CHECKIN_(RUNTIME|HISTORY)_.*_PASS' "$owned/acceptance.log"
printf 'M8_CHECKIN_RUNTIME_ACCEPTANCE_PASS parents=4 skipped=0 PG=18.6 app=REAL native=OWNED_HTTP auth=REAL_COOKIE attendance_GET=0 history=REAL_COOKIE_AFTER_APP_STOP UI=NOT_COVERED private_PT=NOT_COVERED\n'
