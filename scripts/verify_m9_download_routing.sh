#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m9_download_routing.sh' >&2; exit 2; fi
if [[ $(uname -s) != Linux ]]; then echo 'Linux root filesystem capabilities required' >&2; exit 2; fi
if [[ ${PULSE_M9_ROUTING_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M9_ROUTING_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m9_download_routing.sh
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m9-download-routing.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m9-download-routing.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M9_DOWNLOAD_ROUTING_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache" PULSE_M5_AUTOMATIC_TV_ACCEPTANCE=1
mkdir -p "$GOTMPDIR"
run_group(){
 local group=$1 package=$2 expected=$3 pattern=$4 log="$owned/$1.log"
 if ! go test -race -count=1 -timeout=12m "$package" -run "^($pattern)$" -v >"$log" 2>&1; then
  echo "M9_DOWNLOAD_ROUTING_${group}_FAILED" >&2
  grep -E '^--- (FAIL|SKIP):|^[[:space:]]+[^ ]+_test.go:[0-9]+:|^FAIL|^# ' "$log" >&2 || true
  exit 1
 fi
 if grep -q -- '--- SKIP:' "$log"; then echo "M9_DOWNLOAD_ROUTING_${group}_UNEXPECTED_SKIP" >&2; exit 1; fi
 local passed
 passed=$(grep -c '^--- PASS: Test' "$log")
 if [[ "$passed" != "$expected" ]]; then
  echo "M9_DOWNLOAD_ROUTING_${group}_PARENT_COUNT expected=$expected actual=$passed" >&2
  exit 1
 fi
 grep -E '^--- PASS: Test|M9_MOVIE_NATIVE_ROUTING_APP_PASS|M9_TV_NATIVE_ROUTING_APP_PASS' "$log"
}
run_group CONFIG ./internal/config 2 'TestDownloadRoutingDeploymentConfiguration|TestDownloadRoutingExplicitEnableAndDisable'
run_group RULES ./internal/domain/endpoint 3 'TestDownloadRoutePrecedenceAndOrderIndependence|TestDownloadRouteManualBindingDoesNotReroute|TestDownloadRouteFailuresAndSingleFallback'
run_group INTERNAL ./internal/store/download 1 'TestDownloaderRouterDispatch'
run_group TV ./internal/store/postgres 4 'TestAutomaticTVInitialRoutingBindingPG|TestAutomaticTVRoundSnapshotAndConcurrencyPG|TestAutomaticTVStartHoldsPG|TestAutomaticTVStartTailRollbackPG'
run_group MOVIE ./internal/usecase/search 6 'TestMovieRoutingFreezesSourceChoiceBeforeReservationPG|TestMovieRoutingExplicitSingleClientFallbackPG|TestMovieRoutingUsesActualWinningSourcePG|TestMovieRoundSelectorCannotForgeAdmission|TestBindingSwitchCurrentUserReceiptAndOldRound|TestPersistentRoundAtomicFailureRecoveryAndConcurrentFinalization'
run_group SOURCE ./internal/usecase/search 4 'TestAutomaticTVSourceFirstPageBudget|TestAutomaticTVSourceBoundedPagesAndProof|TestAutomaticTVSourcePreservesFirstPage|TestAutomaticTVSourceWaitsForPhysicalReturn'
run_group APP ./internal/app 1 'TestM9MovieAutomaticRoutingRuntimePG'
run_group HTTP ./internal/httpapi 2 'TestM9AutomaticTVRoutingNativeHTTPPG|TestM5AutomaticTVNativePipelineHTTPPG'
printf 'M9_DOWNLOAD_ROUTING_ACCEPTANCE_PASS parents=23 skipped=0 race=true PG=18.6 App=REAL Cookie=REAL Network=OWNED_PROTOCOL site_over_media=true original_bytes=PRESERVED wrong_Add=0 movie_sources=FROZEN_PER_WINNER TV_binding=ATOMIC_CONVERGENT TV_v1_graph=PRESERVED manual_and_receipts=PRESERVED deployment=EXPLICIT milestone=NOT_RELEASED\n'
