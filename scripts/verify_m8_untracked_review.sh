#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m8_untracked_review.sh' >&2; exit 2; fi
if [[ $(uname -s) != Linux ]]; then echo 'Linux root filesystem capabilities required' >&2; exit 2; fi
if [[ ${PULSE_M8_UNTRACKED_REVIEW_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M8_UNTRACKED_REVIEW_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m8_untracked_review.sh
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m8-untracked-review.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m8-untracked-review.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M8_UNTRACKED_REVIEW_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache" PULSE_M8_UNTRACKED_REVIEW_ACCEPTANCE=1
mkdir -p "$GOTMPDIR"
run_group(){
 local group=$1 package=$2 expected=$3 log="$owned/$1.log"
 if ! go test -race -count=1 -timeout=12m "$package" -run '^TestUntrackedReview|^TestUntrackedConfirmation|^TestM8UntrackedReviewRuntimeCookiePG$' -v >"$log" 2>&1; then
  echo "M8_UNTRACKED_REVIEW_${group}_FAILED" >&2
  grep -E '^--- (FAIL|SKIP):|^[[:space:]]+[^ ]+_test.go:[0-9]+:|^FAIL|^# ' "$log" >&2 || true
  exit 1
 fi
 if grep -q -- '--- SKIP:' "$log"; then echo "M8_UNTRACKED_REVIEW_${group}_UNEXPECTED_SKIP" >&2; exit 1; fi
 test "$(grep -c '^--- PASS: Test' "$log")" = "$expected"
 grep -E '^--- PASS: Test|M8_UNTRACKED_REVIEW_APP_PASS' "$log"
}
run_group USECASE ./internal/usecase/library 19
run_group HTTP ./internal/httpapi 13
run_group STORE ./internal/store/postgres 27
run_group APP ./internal/app 1
printf 'M8_UNTRACKED_REVIEW_ACCEPTANCE_PASS parents=60 skipped=0 PG=18.6 app=REAL cookie=REAL rootfs=REAL metadata=NATIVE_OWNED_HTTP movie_and_sparse_tv_rules=true explicit_confirmation=true original_receipt_offline=true late_metadata_fenced=true changed_file_fenced=true downloader_calls=0 imported=0 execution_allowed=false organization=NOT_IMPLEMENTED NAS=NOT_COVERED milestone=NOT_RELEASED\n'
