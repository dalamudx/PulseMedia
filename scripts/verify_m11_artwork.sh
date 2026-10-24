#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-all}
case "$mode" in pure|store|app|all);;*)echo 'Usage: verify_m11_artwork.sh [pure|store|app|all]' >&2;exit 2;;esac
run_group(){
 local label=$1 package=$2 expected=$3 pattern=$4 skip=${5:-'^$'}
 local log="$PULSE_M11_ARTWORK_WORK/$label.log"
 if ! go test -race "$package" -run "$pattern" -skip "$skip" -count=1 -timeout=7m -v >"$log" 2>&1;then tail -n 130 "$log";return 1;fi
 local count;count=$(grep -c '^--- PASS: Test' "$log" || true)
 if [[ "$count" != "$expected" ]] || grep -q -- '--- SKIP:' "$log";then tail -n 100 "$log";printf 'bad parent count %s=%s expected=%s\n' "$label" "$count" "$expected" >&2;return 1;fi
 grep -E '^--- PASS:|M11_.*PASS' "$log" || true
 printf 'M11_ARTWORK_GROUP_PASS label=%s parents=%s skipped=0 race=true\n' "$label" "$count"
}
if [[ ${PULSE_M11_ARTWORK_PG_STAGE:-} == 1 ]];then
 : "${PULSE_TEST_DATABASE_URL:?owned PG required}"
 if [[ "$mode" == store ]];then
  run_group store_1 ./internal/store/mediasidecar 10 '^Test(ArtworkCompletionPG|ArtworkInvalidCompletionRollbackPG|MovieAndTVSnapshotAndCompletion|LeasePayloadEventAndAuthorityFences|SnapshotChangesAndOrdinaryScans|CopiedBindingCannotForgeAnyDTOField|FullMappingsAndOptionalArtwork|ArtifactValidationLeavesGraphUnchanged|UncertainAndObservedResults|AuditAndDeferredTailFailuresRollbackAndRetry)$'
  run_group store_2 ./internal/store/mediasidecar 8 '^Test(LeaseExpiresDuringJobAndBusinessLockWait|ChangesDuringHeldPhysicalWorkAreRejected|ExpiredRecoveryNeverRequeues|ContextCancellationAndSafeErrors|MediaSubtitleImportedFactsAndResults|MediaSubtitleAuthorityFences|MediaSubtitleRejectForgedResults|MediaSubtitleRollback)$'
  run_group store_3 ./internal/store/mediasidecar 5 '^Test(MediaSubtitleLockRaces|MediaSubtitleConcurrentEnqueue|StatFingerprintParsing|OpaquePosterAndRelativePaths|PayloadRejectsAlienShape)$'
  run_group settings ./internal/usecase/settings 29 '^Test' '^TestTVDB'
 elif [[ "$mode" == app ]];then
  run_group movie ./internal/app 1 '^TestM11ArtworkMovieIngestionPG$'
  run_group tv ./internal/httpapi 1 '^TestM11ArtworkTVNativeHTTPPG$'
  run_group tv_compat ./internal/httpapi 1 '^TestM5AutomaticTVNativePipelineHTTPPG$'
  run_group settings_http_compat ./internal/httpapi 1 '^TestSettingsHTTPOptimisticLockAndIdempotency$'
 else
  echo 'invalid PG group' >&2;exit 2
 fi
 exit 0
fi
work=$(mktemp -d /data/temp/pulse-m11-artwork-formal.XXXXXX)
cleanup(){ rc=$?;test ! -L "$work";test "$(realpath -- "$work")" = "$work";rm -rf -- "$work";test ! -e "$work" || exit 1;echo M11_ARTWORK_FORMAL_TEMP_CLEANUP_VERIFIED;exit "$rc"; }
trap cleanup EXIT
export PULSE_M11_ARTWORK_WORK="$work" TMPDIR="$work" TMP="$work" TEMP="$work"
export PULSE_M5_AUTOMATIC_TV_ACCEPTANCE=1
parents=0
if [[ "$mode" == pure || "$mode" == all ]];then
 run_group fanart ./internal/adapter/metadata/fanart 8 '^Test'
 # Preserve the M11-01 slice; TVDB additions are verified by test-m11-tvdb.
 run_group tmdb ./internal/adapter/metadata/tmdb 42 '^Test' '^Test(TVDBEpisode|TVTranslation)'
 run_group domain ./internal/domain/artwork 1 '^Test'
 run_group usecase ./internal/usecase/metadatasidecar 21 '^Test'
 run_group filesystem ./internal/platform/rootfs 16 '^TestSidecar'
 run_group configuration_session ./internal/app 3 '^TestFanartSession'
 run_group settings_cache ./internal/httpapi 1 '^TestSettingsNoStore$'
 parents=$((parents+92))
fi
if [[ "$mode" == store || "$mode" == all ]];then
 PULSE_M11_ARTWORK_PG_STAGE=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m11_artwork.sh store
 parents=$((parents+52))
fi
if [[ "$mode" == app || "$mode" == all ]];then
 PULSE_M11_ARTWORK_PG_STAGE=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m11_artwork.sh app
 parents=$((parents+4))
fi
printf 'M11_ARTWORK_FORMAL_PASS mode=%s parents=%s skipped=0 race=true\n' "$mode" "$parents"
