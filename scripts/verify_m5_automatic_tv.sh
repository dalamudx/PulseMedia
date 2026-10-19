#!/usr/bin/env bash
# Run after ALL_RUNTIME_READY: mise exec -- bash scripts/verify_m5_automatic_tv.sh
# Native HTTP only; existing standard go:embed build prerequisites must be present.
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m5_automatic_tv.sh' >&2; exit 2; fi
# Set opt-in before any database creation. The wrapper always owns a private PG18.6.
export PULSE_M5_AUTOMATIC_TV_ACCEPTANCE=1
if [[ ${PULSE_M5_AUTOMATIC_TV_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M5_AUTOMATIC_TV_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m5_automatic_tv.sh
fi
: "${PULSE_TEST_DATABASE_URL:?private PostgreSQL fixture required}"
owned=$(mktemp -d /data/temp/pulse-m5-automatic-tv.XXXXXX)
cleanup() {
 result=$?
 trap - EXIT
 case "$owned" in
  /data/temp/pulse-m5-automatic-tv.*) test ! -L "$owned"; rm -rf -- "$owned" ;;
  *) exit 98 ;;
 esac
 test ! -e "$owned" || exit 1
 echo M5_AUTOMATIC_TV_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
module_cache=$(mise exec -- go env GOMODCACHE)
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache" GOMODCACHE="$owned/modules"
# Read the existing module download archive; extract/build only in owned caches.
# go.sum verification stays enabled, and the file proxy makes this run offline.
export GOPROXY="file://$module_cache/cache/download"
mkdir -p "$GOTMPDIR" "$GOCACHE" "$GOMODCACHE"
pattern='^TestM5AutomaticTV(NativePipeline|LostAddResponse|SendPublicationRollback|HardlinkPublicationRollback|DisabledOwnerAfterAdd)HTTPPG$'
if ! mise exec -- go test -race \
 ./internal/httpapi/m5_indexer_acceptance_test.go \
 ./internal/httpapi/m5_interactive_search_acceptance_test.go \
 ./internal/httpapi/m5_subscription_creation_acceptance_test.go \
 ./internal/httpapi/m5_automatic_tv_acceptance_test.go \
 -run "$pattern" -count=1 -v -timeout=9m >"$owned/acceptance.log" 2>&1; then
 tail -n 90 "$owned/acceptance.log"
 exit 1
fi
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M5_AUTOMATIC_TV_UNEXPECTED_SKIP >&2; exit 1; fi
actual=$(grep -c '^--- PASS: TestM5AutomaticTV' "$owned/acceptance.log")
if [[ "$actual" != 5 ]]; then echo "M5_AUTOMATIC_TV_MISSING_EXECUTION expected=5 actual=$actual" >&2; exit 1; fi
grep -E 'M5_AUTOMATIC_TV_|--- PASS:|^ok[[:space:]]' "$owned/acceptance.log"
echo "M5_AUTOMATIC_TV_ACCEPTANCE_EXECUTED tests=$actual"
