#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-all}
case "$mode" in pure|store|app|all);;*) echo 'Usage: verify_m11_audit.sh [pure|store|app|all]' >&2;exit 2;;esac
run_group(){
 local label=$1 package=$2 expected=$3 pattern=$4
 local log="$PULSE_M11_AUDIT_WORK/$label.log"
 if ! go test -race "$package" -run "$pattern" -count=1 -timeout=4m -v >"$log" 2>&1;then tail -n 130 "$log";return 1;fi
 local count;count=$(grep -c '^--- PASS: Test' "$log" || true)
 if [[ "$count" != "$expected" ]] || grep -Eq -- '--- SKIP:|--- FAIL:|WARNING: DATA RACE' "$log";then tail -n 100 "$log";printf 'bad parent count %s=%s expected=%s\n' "$label" "$count" "$expected" >&2;return 1;fi
 grep -E '^--- PASS:|M11_AUDIT_.*PASS' "$log" || true
 printf 'M11_AUDIT_GROUP_PASS label=%s parents=%s skipped=0 race=true\n' "$label" "$count"
}
if [[ ${PULSE_M11_AUDIT_PG_STAGE:-} == 1 ]];then
 : "${PULSE_TEST_DATABASE_URL:?owned PG required}"
 if [[ "$mode" == store || "$mode" == all ]];then
  run_group timeline_store ./internal/store/postgres 6 '^TestAuditTimeline.*PG$'
  run_group legacy_persistence ./internal/usecase/audit 1 '^TestAuditLogPersistence$'
 fi
 if [[ "$mode" == app || "$mode" == all ]];then
  run_group public_cookie ./internal/httpapi 2 '^TestM11Audit(CookiePaginationAndPermissionPG|BusinessAtomicityAndUnavailablePG)$'
 fi
 exit 0
fi
work=$(mktemp -d /data/temp/pulse-m11-audit-formal.XXXXXX)
cleanup(){ rc=$?;test ! -L "$work";test "$(realpath -- "$work")" = "$work";rm -rf -- "$work";test ! -e "$work" || exit 1;echo M11_AUDIT_FORMAL_TEMP_CLEANUP_VERIFIED;exit "$rc"; }
trap cleanup EXIT
export PULSE_M11_AUDIT_WORK="$work" TMPDIR="$work" TMP="$work" TEMP="$work"
export PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE=1
parents=0
if [[ "$mode" == pure || "$mode" == all ]];then
 run_group rules ./internal/domain/auditlog 4 '^TestAudit(Cursor|Filter|Position)'
 run_group service ./internal/usecase/audit 4 '^TestAuditTimeline'
 run_group public_http ./internal/httpapi 4 '^TestAuditTimelineHTTP'
 parents=$((parents+12))
fi
if [[ "$mode" != pure ]];then
 if ! PULSE_M11_AUDIT_PG_STAGE=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m11_audit.sh "$mode" >"$work/postgres.log" 2>&1;then tail -n 130 "$work/postgres.log";exit 1;fi
 cat "$work/postgres.log"
 grep -q '^OWNED_PODMAN_TEST_DATABASE_CLEANUP_VERIFIED' "$work/postgres.log"
 grep -q '^OWNED_PODMAN_TEST_CONTAINER_AND_TEMP_CLEANUP_VERIFIED' "$work/postgres.log"
 case "$mode" in store) parents=$((parents+7));;app) parents=$((parents+2));;all) parents=$((parents+9));;esac
fi
printf 'M11_AUDIT_FORMAL_PASS mode=%s parents=%s skipped=0 race=true\n' "$mode" "$parents"
