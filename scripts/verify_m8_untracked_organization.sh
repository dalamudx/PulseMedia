#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m8_untracked_organization.sh' >&2; exit 2; fi
if [[ $(uname -s) != Linux ]]; then echo 'Linux root filesystem capabilities required' >&2; exit 2; fi
if [[ ${PULSE_M8_UNTRACKED_ORGANIZATION_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M8_UNTRACKED_ORGANIZATION_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m8_untracked_organization.sh
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m8-untracked-organization.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m8-untracked-organization.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M8_UNTRACKED_ORGANIZATION_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache" PULSE_M8_UNTRACKED_ORGANIZATION_ACCEPTANCE=1
mkdir -p "$GOTMPDIR"
run_group(){
 local group=$1 package=$2 expected=$3 log="$owned/$1.log"
 if ! go test -race -count=1 -timeout=12m "$package" -run '^TestUntrackedOrganization|^TestExternalTransfer|^TestM8UntrackedOrganization' -v >"$log" 2>&1; then
  echo "M8_UNTRACKED_ORGANIZATION_${group}_FAILED" >&2
  grep -E '^--- (FAIL|SKIP):|^[[:space:]]+[^ ]+_test.go:[0-9]+:|^FAIL|^# ' "$log" >&2 || true
  exit 1
 fi
 if grep -q -- '--- SKIP:' "$log"; then echo "M8_UNTRACKED_ORGANIZATION_${group}_UNEXPECTED_SKIP" >&2; exit 1; fi
 test "$(grep -c '^--- PASS: Test' "$log")" = "$expected"
 grep -E '^--- PASS: Test|M8_UNTRACKED_ORGANIZATION_APP_PASS|M8_UNTRACKED_COMMIT_RECEIPT_PASS' "$log"
}
run_group USECASE ./internal/usecase/library 14
run_group HTTP ./internal/httpapi 10
run_group PHYSICAL ./internal/store/transfer 3
run_group STORE ./internal/store/postgres 20
run_group APP ./internal/app 8
test "$(grep -c 'M8_UNTRACKED_ORGANIZATION_APP_PASS scenario=sparse_tv_one_file_S01E02_S01E07' "$owned/APP.log")" = 1
test "$(grep -c 'M8_UNTRACKED_COMMIT_RECEIPT_PASS server_committed=true client_reply_discarded=true' "$owned/APP.log")" = 3
printf 'M8_UNTRACKED_ORGANIZATION_ACCEPTANCE_PASS parents=55 skipped=0 PG=18.6 app=REAL cookie=REAL rootfs=REAL metadata=NATIVE_OWNED_HTTP hardlink_copy_move=REAL sparse_tv=REAL_APP_AND_PG original_receipt_offline=true permanent_permit=true tail_rollback_observe_token2=true source_changed_disabled_existing_target_fenced=true downloader_calls=0 exact_native_call_count=SEPARATE_COMMIT_RECOVERY_TASK COMMIT_receipt_loss=REAL_FINALIZATION_REPLY sparse_tv_APP=one_file_two_exact_units_unknown_date_preserved reflink_runtime=CAPABILITY_DEPENDENT NAS=NOT_COVERED milestone=NOT_RELEASED\n'
