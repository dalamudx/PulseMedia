#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-all}
case "$mode" in pure|store|app|all);;*) echo 'Usage: verify_m11_web_push.sh [pure|store|app|all]' >&2;exit 2;;esac
run_group(){
 local label=$1 package=$2 expected=$3 pattern=$4 log
 log="$PULSE_M11_PUSH_WORK/$label.log"
 if ! go test -race "$package" -run "$pattern" -count=1 -timeout=5m -v >"$log" 2>&1;then tail -n 110 "$log";return 1;fi
 local count;count=$(grep -c '^--- PASS: Test' "$log" || true)
 if [[ "$count" != "$expected" ]] || grep -Eq -- '--- SKIP:|--- FAIL:|WARNING: DATA RACE' "$log";then tail -n 90 "$log";printf 'bad parent count %s=%s expected=%s\n' "$label" "$count" "$expected" >&2;return 1;fi
 grep -E '^--- PASS:|M11_.*PASS' "$log" || true
 printf 'M11_PUSH_GROUP_PASS label=%s parents=%s skipped=0 race=true\n' "$label" "$count"
}
if [[ ${PULSE_M11_PUSH_PG_STAGE:-} == 1 ]];then
 : "${PULSE_TEST_DATABASE_URL:?owned PG required}"
 if [[ "$mode" == store || "$mode" == all ]];then
  run_group devices ./internal/store/postgres 5 '^TestWebPushDevice.*PG$'
  run_group deliveries ./internal/store/postgres 4 '^TestWebPushDelivery.*PG$'
 fi
 if [[ "$mode" == app || "$mode" == all ]];then
  run_group cookie ./internal/httpapi 2 '^TestM11WebPushRegistration.*PG$'
  run_group native_movie ./internal/app 1 '^TestM11WebPushMovieIngestionPG$'
  run_group legacy_ecosystem ./internal/app 1 '^TestFullM11EcosystemIntegration$'
 fi
 exit 0
fi
work=$(mktemp -d /data/temp/pulse-m11-push-formal.XXXXXX)
cleanup(){ rc=$?;test ! -L "$work";test "$(realpath -- "$work")" = "$work";rm -rf -- "$work";test ! -e "$work" || exit 1;echo M11_PUSH_FORMAL_TEMP_CLEANUP_VERIFIED;exit "$rc"; }
trap cleanup EXIT
export PULSE_M11_PUSH_WORK="$work" TMPDIR="$work" TMP="$work" TEMP="$work"
export PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE=1
parents=0
if [[ "$mode" == pure || "$mode" == all ]];then
 run_group native ./internal/adapter/notifier/webpush 6 '^TestWebPush'
 run_group service ./internal/usecase/notify 8 '^TestWebPush'
 run_group deployment ./internal/config 1 '^TestWebPushDeploymentConfiguration$'
 run_group public_http ./internal/httpapi 4 '^TestWebPushHTTP'
 if ! node --test web/tests/web-push-worker.test.mjs web/tests/web-push-settings.test.mjs >"$work/frontend.log" 2>&1;then cat "$work/frontend.log";exit 1;fi
 test "$(grep -Ec '^ℹ tests 12$|^# tests 12$' "$work/frontend.log")" = 1
 test "$(grep -Ec '^ℹ fail 0$|^# fail 0$' "$work/frontend.log")" = 1
 test "$(grep -Ec '^ℹ skipped 0$|^# skipped 0$' "$work/frontend.log")" = 1
 echo M11_PUSH_FRONTEND_PASS_checks=12_skipped=0
 parents=19
fi
if [[ "$mode" != pure ]];then
 PULSE_M11_PUSH_PG_STAGE=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m11_web_push.sh "$mode"
 case "$mode" in store) parents=9;;app) parents=4;;all) parents=32;;esac
fi
printf 'M11_PUSH_FORMAL_PASS mode=%s parents=%s skipped=0 race=true\n' "$mode" "$parents"
