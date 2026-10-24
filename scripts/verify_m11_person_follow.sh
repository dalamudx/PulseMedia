#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-all}
case "$mode" in pure|store|app|all);;*) echo 'Usage: verify_m11_person_follow.sh [pure|store|app|all]' >&2;exit 2;;esac
run_group(){
 local label=$1 package=$2 expected=$3 pattern=$4
 local log="$PULSE_M11_PERSON_WORK/$label.log"
 if ! go test -race "$package" -run "$pattern" -count=1 -timeout=7m -v >"$log" 2>&1;then tail -n 100 "$log";return 1;fi
 local count;count=$(grep -c '^--- PASS: Test' "$log" || true)
 if [[ "$count" != "$expected" ]] || grep -q -- '--- SKIP:' "$log";then tail -n 100 "$log";printf 'bad parent count %s=%s expected=%s\n' "$label" "$count" "$expected" >&2;return 1;fi
 grep -E '^--- PASS:|M11_.*PASS' "$log" || true
 printf 'M11_PERSON_GROUP_PASS label=%s parents=%s skipped=0 race=true\n' "$label" "$count"
}
if [[ ${PULSE_M11_PERSON_PG_STAGE:-} == 1 ]];then
 : "${PULSE_TEST_DATABASE_URL:?owned PG required}"
 case "$mode" in
 store)
  run_group configuration ./internal/store/postgres 7 '^TestPersonFollowConfig.*PG$'
  run_group scheduling ./internal/store/postgres 6 '^TestPersonFollowSchedule.*PG$'
  run_group execution ./internal/store/postgres 7 '^TestPersonFollowExecution.*PG$'
  run_group sealed_source ./internal/store/postgres 6 '^TestPersonFollowSource.*PG$'
  run_group creation ./internal/store/postgres 6 '^TestPersonFollowCreation.*PG$'
  run_group recovery ./internal/store/postgres 7 '^TestPersonFollowRecovery.*PG$'
  run_group creation_compatibility ./internal/store/postgres 3 '^(TestSubscriptionCreationMovieTVSparseAndIdentity|TestDoubanWishCreationOriginalKeyConcurrency|TestDoubanWishCreationPreservesExistingIntent)$'
  ;;
 app)
  run_group public_cookie ./internal/httpapi 2 '^TestM11PersonFollowConfiguration.*PG$'
  run_group native_person ./internal/httpapi 2 '^TestM11PersonFollow(NativeMovieTV|LateDisable)PG$'
  run_group native_tv_compatibility ./internal/httpapi 1 '^TestM5AutomaticTVNativePipelineHTTPPG$'
  run_group prototype_compatibility ./internal/app 1 '^TestFullM11EcosystemIntegration$'
  ;;
 *) echo 'invalid PG group' >&2;exit 2;;
 esac
 exit 0
fi
work=$(mktemp -d /data/temp/pulse-m11-person-formal.XXXXXX)
cleanup(){ rc=$?;test ! -L "$work";test "$(realpath -- "$work")" = "$work";rm -rf -- "$work";test ! -e "$work" || exit 1;echo M11_PERSON_FORMAL_TEMP_CLEANUP_VERIFIED;exit "$rc"; }
trap cleanup EXIT
export PULSE_M11_PERSON_WORK="$work" TMPDIR="$work" TMP="$work" TEMP="$work"
export PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE=1 PULSE_M5_AUTOMATIC_TV_ACCEPTANCE=1
parents=0
if [[ "$mode" == pure || "$mode" == all ]];then
 run_group domain ./internal/domain/personfollow 7 '^Test'
 run_group usecase ./internal/usecase/personfollow 13 '^Test'
 run_group tmdb ./internal/adapter/metadata/tmdb 54 '^Test'
 run_group http ./internal/httpapi 5 '^TestPersonFollowHTTP'
 parents=$((parents+79))
fi
if [[ "$mode" == store || "$mode" == all ]];then
 PULSE_M11_PERSON_PG_STAGE=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m11_person_follow.sh store
 parents=$((parents+42))
fi
if [[ "$mode" == app || "$mode" == all ]];then
 PULSE_M11_PERSON_PG_STAGE=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m11_person_follow.sh app
 parents=$((parents+6))
fi
printf 'M11_PERSON_FORMAL_PASS mode=%s parents=%s skipped=0 race=true\n' "$mode" "$parents"
