#!/usr/bin/env bash
# Run from Main: mise exec -- bash scripts/verify_m7_media_scrape.sh
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m7_media_scrape.sh' >&2; exit 2; fi
if [[ ${PULSE_M7_MEDIA_SCRAPE_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M7_MEDIA_SCRAPE_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m7_media_scrape.sh
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m7-media-scrape.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m7-media-scrape.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M7_MEDIA_SCRAPE_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
module_cache=$(mise exec -- go env GOMODCACHE)
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache" GOMODCACHE="$owned/modules"
export GOPROXY="file://$module_cache/cache/download"
export XDG_CACHE_HOME="$owned/cache" XDG_CONFIG_HOME="$owned/config"
mkdir -p "$GOTMPDIR" "$GOCACHE" "$GOMODCACHE"
mise exec -- go test -race ./internal/app -run '^TestM7MediaScrape' -count=1 -v -timeout=300s 2>&1 | tee "$owned/acceptance.log"
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M7_MEDIA_SCRAPE_UNEXPECTED_SKIP >&2; exit 1; fi
for name in PublicCookieContractPG AdminRecheckedAfterLockPG RestoreIsolationPG NativeFilesPG; do
 test "$(grep -c "^--- PASS: TestM7MediaScrape$name (" "$owned/acceptance.log")" = 1
done
test "$(grep -c 'M7_MEDIA_SCRAPE_NATIVE_FILES_PASS' "$owned/acceptance.log")" = 4
test "$(grep -c 'M7_MEDIA_SCRAPE_PUBLIC_COOKIE_PG_PASS' "$owned/acceptance.log")" = 1
echo M7_MEDIA_SCRAPE_ACCEPTANCE_EXECUTED_tests=4_skipped=0_inventory=SQL_PREPARED_execution=native_claimed_lease
