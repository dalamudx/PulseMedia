#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ ${PULSE_M11_ADVANCED_PG_STAGE:-} != 1 ]];then exec env PULSE_M11_ADVANCED_PG_STAGE=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m11_advanced_settings.sh;fi
: "${PULSE_TEST_DATABASE_URL:?owned PG required}"
work=$(mktemp -d /data/temp/pulse-m11-advanced-formal.XXXXXX)
cleanup(){ rc=$?;test ! -L "$work";test "$(realpath -- "$work")" = "$work";rm -rf -- "$work";test ! -e "$work" || exit 1;echo M11_ADVANCED_FORMAL_TEMP_CLEANUP_VERIFIED;exit "$rc"; }
trap cleanup EXIT
export TMPDIR="$work" TMP="$work" TEMP="$work" PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE=1
run_group(){
 local label=$1 package=$2 expected=$3 pattern=$4 log="$work/$1.log"
 if ! go test -race "$package" -run "$pattern" -count=1 -timeout=7m -v >"$log" 2>&1;then tail -n 100 "$log";return 1;fi
 local count;count=$(grep -c '^--- PASS: Test' "$log" || true)
 if [[ "$count" != "$expected" ]] || grep -q -- '--- SKIP:' "$log";then tail -n 100 "$log";exit 1;fi
 grep -E '^--- PASS:|M11_.*PASS' "$log" || true
 printf 'M11_ADVANCED_GROUP_PASS label=%s parents=%s skipped=0 race=true\n' "$label" "$count"
}
run_group fanart_configuration ./internal/usecase/settings 3 '^TestFanart.*PG$'
run_group audit_http ./internal/httpapi 4 '^TestAuditTimelineHTTP'
run_group audit_cookie ./internal/httpapi 2 '^TestM11Audit(CookiePaginationAndPermissionPG|BusinessAtomicityAndUnavailablePG)$'
run_group person_cookie ./internal/httpapi 2 '^TestM11PersonFollowConfiguration.*PG$'
printf 'M11_ADVANCED_FORMAL_PASS parents=11 skipped=0 race=true\n'
