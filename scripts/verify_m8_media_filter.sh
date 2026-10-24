#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m8_media_filter.sh' >&2; exit 2; fi
if [[ $(uname -s) != Linux ]]; then echo 'Linux root filesystem capabilities required' >&2; exit 2; fi
if [[ ${PULSE_M8_MEDIA_FILTER_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M8_MEDIA_FILTER_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m8_media_filter.sh
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m8-media-filter.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m8-media-filter.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M8_MEDIA_FILTER_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache" PULSE_M8_UNTRACKED_ORGANIZATION_ACCEPTANCE=1
mkdir -p "$GOTMPDIR"
run_group(){
 local group=$1 package=$2 expected=$3 pattern=$4 log="$owned/$1.log"
 if ! go test -race -count=1 -timeout=12m "$package" -run "^($pattern)$" -v >"$log" 2>&1; then
  echo "M8_MEDIA_FILTER_${group}_FAILED" >&2
  grep -E '^--- (FAIL|SKIP):|^[[:space:]]+[^ ]+_test.go:[0-9]+:|^FAIL|^# ' "$log" >&2 || true
  exit 1
 fi
 if grep -q -- '--- SKIP:' "$log"; then echo "M8_MEDIA_FILTER_${group}_UNEXPECTED_SKIP" >&2; exit 1; fi
 local passed
 passed=$(grep -c '^--- PASS: Test' "$log")
 if [[ "$passed" != "$expected" ]]; then
  echo "M8_MEDIA_FILTER_${group}_PARENT_COUNT expected=$expected actual=$passed" >&2
  exit 1
 fi
 grep -E '^--- PASS: Test|M8_MEDIA_SIZE_BOUNDARY_PASS|M8_MEDIA_SIZE_RECOVERY_PASS' "$log"
}
run_group CONFIG ./internal/config 1 'TestOrganizationMinimumMediaBytesConfiguration'
run_group RULES ./internal/domain/naming 5 'TestPrimaryMediaPathBoundaries|TestMediaSizeMustBeMeasuredAndMeetThreshold|TestIsEligibleMediaFileRules|TestMovieDirectoryFiltersSamplesButKeepsAllEvidence|TestMovieDirectorySelectionCompleteAndUnique'
run_group TRANSFER ./internal/store/transfer 13 'TestMediaMinimumRechecksPendingPlanBeforeNativeWritePG|TestMediaFilterRunningStepOnlyObservesOriginalTargetPG|TestAutomaticTVOrganizationMediaSizeBoundaryPG|TestAutomaticTVOrganizationNativeAtomicPG|TestLibraryReuseMediaFilterConfirmationPG|TestLibraryReuseMediaFilterPendingPlanPG|TestLibraryReuseMediaFilterPublishedRecoveryPG|TestUpgradeSharedFilePreservesEveryUnit|TestUpgradeMediaFileAtomicSwitchAndFactRevocation|TestLibraryReuseExecutionPG|TestLibraryReuseCommitFailureRecoveryPG|TestLibraryReuseFullMappingPG|TestLibraryReusePreviewTargetIsolationPG'
run_group HTTP ./internal/httpapi 2 'TestCompleteTVOrganizationMediaSizeBoundaryPG|TestCompleteTVOrganizationPGTwoVideosAndEightWayReceipt'
run_group APP ./internal/app 12 'TestM8MovieDirectoryFiltersSampleRuntimePG|TestM8OrganizationMeasuredSizeBoundaryRuntimeCookiePG|TestM8OrganizationPublishedRecoveryAfterMinimumIncreaseRuntimeCookiePG|TestM8MovieRecognitionFilterKeepsScanInventoryPG|TestM8MovieRecognitionFilterRechecksQueuedAndCapturedInputPG|TestM8MovieRecognitionFilterDoesNotStarveLaterPageOrRootPG|TestM8MovieRecognitionAtMinimumAndWordBoundaryPG|TestMovieDirectoryEvidenceAtomicAndScoped|TestAppRunCompleteMovieDirectory|TestAppRunFullMoviePipelineUsesOnePhysicalAcquisition|TestMovieLinkReplayKeepsOneImportedFact|TestMovieRecognitionQueueAdmissionAndEightWay'
printf 'M8_MEDIA_FILTER_ACCEPTANCE_PASS parents=33 skipped=0 PG=18.6 race=true App=REAL Cookie=REAL rootfs=REAL sample_evidence=COMPLETE original_bytes=PRESERVED size=MEASURED_POSITIVE_INCLUSIVE configured_minimum=DEPLOYMENT_POSITIVE_INT64 scan_inventory=PRESERVED recognition=PRE_NETWORK_AND_COMMIT paging=ROOT_AND_FILE_KEYSET automatic_tv=MEASURED_BOUNDARY bundle=ALL_EXACT_UNITS reuse=FILTERED_CONFIRM_AND_OBSERVE_ONLY_RECOVERY running_plan=NO_NEW_WRITE_AFTER_TIGHTENING published_recovery=HARDLINK_COPY_MOVE original_receipts=PRESERVED upgrade=LEGACY_COMPATIBILITY milestone=NOT_RELEASED\n'
