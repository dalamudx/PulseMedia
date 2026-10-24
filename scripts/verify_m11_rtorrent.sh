#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-all}
case "$mode" in pure|store|app|all);;*) echo 'Usage: verify_m11_rtorrent.sh [pure|store|app|all]' >&2;exit 2;;esac
run_group(){
 local label=$1 package=$2 expected=$3 pattern=$4
 local log="$PULSE_M11_RTORRENT_WORK/$label.log"
 if ! go test -race "$package" -run "$pattern" -count=1 -timeout=4m -v >"$log" 2>&1;then tail -n 130 "$log";return 1;fi
 local count;count=$(grep -c '^--- PASS: Test' "$log" || true)
 if [[ "$count" != "$expected" ]] || grep -Eq -- '--- SKIP:|--- FAIL:|WARNING: DATA RACE' "$log";then tail -n 100 "$log";printf 'bad parent count %s=%s expected=%s\n' "$label" "$count" "$expected" >&2;return 1;fi
 grep -E '^--- PASS:|M11_RTORRENT_.*PASS' "$log" || true
 printf 'M11_RTORRENT_GROUP_PASS label=%s parents=%s skipped=0 race=true\n' "$label" "$count"
}
if [[ ${PULSE_M11_RTORRENT_PG_STAGE:-} == 1 ]];then
 : "${PULSE_TEST_DATABASE_URL:?owned PG required}"
 if [[ "$mode" == store || "$mode" == all ]];then
  run_group configuration ./internal/store/postgres 3 '^TestRTorrent(ConfigurationPersistencePG|ConfigurationTailRollbackPG|Schema119HistoryPG)$'
  run_group historical_contracts ./internal/store/postgres 3 '^Test(EndpointConfigurationNativeRoundtripPG|EndpointConfigurationValidationPG|AuditTimelineBusinessRollbackAndSchemaHistoryPG)$'
 fi
 if [[ "$mode" == app || "$mode" == all ]];then
  run_group native_movie ./internal/app 2 '^TestM11RTorrentMovie(Native|LostReceiptRecovery)PG$'
  run_group public_cookie ./internal/httpapi 1 '^TestM11RTorrentConfigurationCookiePG$'
  run_group movie_compatibility ./internal/app 1 '^TestAppRunFullMoviePipelineUsesOnePhysicalAcquisition$'
  run_group prototype_compatibility ./internal/app 1 '^TestFullM11EcosystemIntegration$'
 fi
 exit 0
fi
work=$(mktemp -d /data/temp/pulse-m11-rtorrent-formal.XXXXXX)
cleanup(){ rc=$?;test ! -L "$work";test "$(realpath -- "$work")" = "$work";rm -rf -- "$work";test ! -e "$work" || exit 1;echo M11_RTORRENT_FORMAL_TEMP_CLEANUP_VERIFIED;exit "$rc"; }
trap cleanup EXIT
export PULSE_M11_RTORRENT_WORK="$work" TMPDIR="$work" TMP="$work" TEMP="$work"
export PULSE_M5_ENDPOINT_CONFIGURATION_ACCEPTANCE=1
parents=0
if [[ "$mode" == pure || "$mode" == all ]];then
 run_group native_rpc ./internal/adapter/downloader/rtorrent 12 '^Test(RPC|RTorrent)'
 parents=$((parents+12))
fi
if [[ "$mode" != pure ]];then
 if ! PULSE_M11_RTORRENT_PG_STAGE=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m11_rtorrent.sh "$mode" >"$work/postgres.log" 2>&1;then tail -n 130 "$work/postgres.log";exit 1;fi
 cat "$work/postgres.log"
 grep -q '^OWNED_PODMAN_TEST_DATABASE_CLEANUP_VERIFIED' "$work/postgres.log"
 grep -q '^OWNED_PODMAN_TEST_CONTAINER_AND_TEMP_CLEANUP_VERIFIED' "$work/postgres.log"
 case "$mode" in store) parents=$((parents+6));;app) parents=$((parents+5));;all) parents=$((parents+11));;esac
fi
printf 'M11_RTORRENT_FORMAL_PASS mode=%s parents=%s skipped=0 race=true\n' "$mode" "$parents"
