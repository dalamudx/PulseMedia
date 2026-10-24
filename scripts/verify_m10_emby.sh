#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
run_group(){
 local label=$1 package=$2 expected=$3 pattern=$4
 local log="$PULSE_M10_EMBY_WORK/$label.log"
 if ! go test -race "$package" -run "$pattern" -count=1 -timeout=8m -v >"$log" 2>&1;then tail -n 130 "$log";return 1;fi
 local count;count=$(grep -c '^--- PASS: Test' "$log" || true)
 if [[ "$count" != "$expected" ]] || grep -q -- '--- SKIP:' "$log";then tail -n 100 "$log";printf 'bad parent count %s=%s expected=%s\n' "$label" "$count" "$expected" >&2;return 1;fi
 grep -E '^--- PASS:|M10_.*PASS' "$log" || true
 printf 'M10_EMBY_GROUP_PASS label=%s parents=%s skipped=0\n' "$label" "$count"
}
if [[ ${PULSE_M10_EMBY_PG_STAGE:-} == 1 ]];then
 : "${PULSE_TEST_DATABASE_URL:?owned PG required}"
 run_group app ./internal/app 3 '^TestM10(ThreeMediaServersNativeRuntime|EmbyRateLimitIndependentRuntime|EmbyConfirmationNative)PG$'
 run_group confirmation_compat ./internal/app 5 '^Test(MediaConfirmation(EightWayWritesOnce|RejectsChangedInputs|TailFailureRollsBack|RechecksAfterRootLock)|NativeJellyfinConfirmationRequiresExactPathProof)$'
 run_group public ./internal/httpapi 5 '^TestM5EndpointConfiguration(NativeRoundTrip|NativeFailureClasses|PermissionsZeroIO|AccountLockFence|ChangedDuringNativeProbe)HTTPPG$'
 run_group usecase ./internal/usecase/mediaserver 7 '^Test'
 exit 0
fi
work=$(mktemp -d /data/temp/pulse-m10-emby-formal.XXXXXX)
cleanup(){ rc=$?;rm -rf -- "$work";test ! -e "$work" || exit 1;echo M10_EMBY_FORMAL_TEMP_CLEANUP_VERIFIED;exit "$rc"; }
trap cleanup EXIT
export PULSE_M10_EMBY_WORK="$work" TMPDIR="$work" TMP="$work" TEMP="$work"
export PULSE_M5_ENDPOINT_CONFIGURATION_ACCEPTANCE=1
run_group emby ./internal/adapter/mediaserver/emby 8 '^TestEmby'
run_group jellyfin ./internal/adapter/mediaserver/jellyfin 3 '^TestJellyfin(RefreshAndFindByPath|SafeTypedHTTPFailures|ExactPathProof)$'
run_group plex ./internal/adapter/mediaserver/plex 2 '^TestPlex'
PULSE_M10_EMBY_PG_STAGE=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m10_emby.sh
printf 'M10_EMBY_FORMAL_PASS parents=33 skipped=0 PG=18.6 race=true\n'
