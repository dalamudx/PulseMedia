#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# -gt 1 ]]; then echo 'Usage: verify_m9_global_rates.sh [all|backend|ui]' >&2; exit 2; fi
mode=${1:-all}
case "$mode" in all|backend|ui) ;; *) echo 'Expected all, backend or ui.' >&2; exit 2;; esac
if [[ "$mode" == ui ]]; then exec env PULSE_M9_RATE_BROWSER=1 bash scripts/verify_m9_global_rates_ui.sh; fi
if [[ ${PULSE_M9_RATES_OWNED_PG:-} != 1 ]]; then exec env PULSE_M9_RATES_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m9_global_rates.sh "$mode"; fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m9-global-rates.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m9-global-rates.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M9_GLOBAL_RATES_FORMAL_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache" PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE=1
mkdir -p "$GOTMPDIR"
run_group(){
 local group=$1 package=$2 expected=$3 pattern=$4 log="$owned/$1.log"
 if ! go test -race -count=1 -timeout=10m "$package" -run "^($pattern)$" -v >"$log" 2>&1; then
  echo "M9_GLOBAL_RATES_${group}_FAILED" >&2
  grep -E '^--- (FAIL|SKIP):|^[[:space:]]+[^ ]+_test.go:[0-9]+:|^FAIL|^# ' "$log" >&2 || true
  exit 1
 fi
 if grep -q -- '--- SKIP:' "$log"; then echo "M9_GLOBAL_RATES_${group}_UNEXPECTED_SKIP" >&2; exit 1; fi
 local passed
 passed=$(grep -c '^--- PASS: Test' "$log")
 if [[ "$passed" != "$expected" ]]; then echo "M9_GLOBAL_RATES_${group}_PARENT_COUNT expected=$expected actual=$passed" >&2; exit 1; fi
 grep -E '^--- PASS: Test|M9_GLOBAL_RATE_(NATIVE_APP|HTTP_READ)_PASS' "$log"
}
run_group CONFIG ./internal/config 1 'TestDownloadGlobalRatesExplicitSwitch'
run_group QB ./internal/adapter/downloader/qbittorrent 2 'TestGlobalTransferRatesNative|TestGlobalTransferRatesFailures'
run_group TRANSMISSION ./internal/adapter/downloader/transmission 3 'TestGlobalTransferRatesNative|TestGlobalTransferRatesFailures|TestGlobalTransferRatesRetryAfter'
run_group USECASE ./internal/usecase/downloadmetrics 8 'TestCollectorRecordsOneNativeObservation|TestCollectorFailureDoesNotPublishValues|TestCollectorRejectsInvalidAuthorityBeforeIO|TestCollectorCancellationWaitsForPhysicalReturn|TestGlobalOverviewExactTotalsAndKnownZero|TestGlobalOverviewUnknownDirectionsAndAvailability|TestGlobalOverviewHistoryUsesAsOfEvidenceAndGaps|TestGlobalOverviewAccessAndReadErrors'
run_group POSTGRES ./internal/store/postgres 13 'TestDownloadGlobalRatesMigrationPG|TestDownloadGlobalRatesLifecyclePG|TestDownloadGlobalRatesFailurePreservesLastGoodPG|TestDownloadGlobalRatesConfigChangesRejectLateSamplePG|TestDownloadGlobalRatesLeaseFencingPG|TestDownloadGlobalRatesRecoverWaitsForPhysicalReturnPG|TestDownloadGlobalRatesConcurrentSchedulingPG|TestDownloadGlobalRatesTailRollbackPG|TestDownloadGlobalRatesCommitClockLeasePG|TestDownloadGlobalRatesReadOnlySnapshotPG|TestDownloadGlobalRatesReadFailureAndConfigHistoryPG|TestDownloadGlobalRatesReadCurrentAdministratorPG|TestDownloadGlobalRatesReadDatabaseFailurePG'
run_group HTTP ./internal/httpapi 2 'TestDownloadGlobalRatesNoStore|TestM9GlobalRatesNativeAppCookiePG'
echo M9_GLOBAL_RATES_BACKEND_ACCEPTANCE_PASS_parents=29_skipped=0
if [[ "$mode" == all ]]; then
 (cd web && node --test tests/download-global-rates.test.mjs) > "$owned/frontend.log" 2>&1 || { tail -n 40 "$owned/frontend.log"; exit 1; }
 grep -Eq '(^# tests 12$|^ℹ tests 12$)' "$owned/frontend.log"
 grep -Eq '(^# pass 12$|^ℹ pass 12$)' "$owned/frontend.log"
 grep -Eq '(^# skipped 0$|^ℹ skipped 0$)' "$owned/frontend.log"
 env PULSE_M9_RATE_BROWSER=1 PULSE_M9_RATE_OWNED_PG=1 bash scripts/verify_m9_global_rates_ui.sh
 echo M9_GLOBAL_RATES_ALL_ACCEPTANCE_PASS_backend_parents=29_Chrome_parents=1_frontend=12_skipped=0
fi
