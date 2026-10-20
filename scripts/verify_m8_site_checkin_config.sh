#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m8_site_checkin_config.sh' >&2; exit 2; fi
if [[ ${PULSE_M8_CHECKIN_CONFIG_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M8_CHECKIN_CONFIG_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m8_site_checkin_config.sh
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m8-checkin-config.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m8-checkin-config.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M8_CHECKIN_CONFIG_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build"
mkdir -p "$GOTMPDIR"
go test -race -count=1 -timeout=240s ./internal/app -run '^TestM8SiteCheckinConfig' -v 2>&1 | tee "$owned/acceptance.log"
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M8_CHECKIN_CONFIG_UNEXPECTED_SKIP >&2; exit 1; fi
for name in CookiePG PermissionsPG ReadFailureAndIsolationPG; do
 test "$(grep -c "^--- PASS: TestM8SiteCheckinConfig$name (" "$owned/acceptance.log")" = 1
done
printf 'M8_CHECKIN_CONFIG_ACCEPTANCE_PASS tests=3 skipped=0 configuration_only=true native_requests=0 checkin_jobs=0\n'
