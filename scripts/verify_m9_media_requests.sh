#!/usr/bin/env bash
set -euo pipefail
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo"
mode=${1:-all}
case "$mode" in backend|browser|all) ;; *) echo 'usage: verify_m9_media_requests.sh [backend|browser|all]' >&2; exit 2 ;; esac
work=$(mktemp -d /data/temp/pulse-m9-media-requests.XXXXXX)
cleanup() {
 local rc=$?
 trap - EXIT
 rm -rf -- "$work"
 if [[ -e "$work" ]]; then echo "owned cleanup failed: $work" >&2; exit 1; fi
 echo M9_MEDIA_REQUESTS_FORMAL_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
export TMPDIR="$work" TMP="$work" TEMP="$work" GOTMPDIR="$work/build"
mkdir -p "$GOTMPDIR"
run_group() {
 local label=$1 pkg=$2 expected=$3 pattern=$4 count
 if ! go test -race "$pkg" -count=1 -timeout=8m -run "^($pattern)$" -v >"$work/$label.log" 2>&1; then
  tail -n 150 "$work/$label.log"
  return 1
 fi
 count=$(grep -c '^--- PASS: Test' "$work/$label.log" || true)
 if [[ "$count" != "$expected" ]] || grep -q -- '--- SKIP:' "$work/$label.log"; then
  tail -n 150 "$work/$label.log"
  echo "$label expected $expected zero-skip parents, got $count" >&2
  return 1
 fi
 grep -E '^--- PASS:|M9_MEDIA_REQUEST|M9_REQUEST' "$work/$label.log" || true
}
if [[ "$mode" != browser ]]; then
 run_group DOMAIN ./internal/domain/mediarequest 4 'TestMediaRequest.*'
 run_group USECASE ./internal/usecase/subscription 6 'TestMediaRequest(SubmitOnlyIntent|ActorAndReadScope|ApprovalUsesSavedIntent|MemberCannotApproveOrReject|ReadFailureAndCancellation|ApprovalReceiptReadOnly)'
 run_group TRANSPORT ./internal/httpapi 7 'TestStrictMediaRequestJSON|TestMediaRequestNumericAndShapeBoundaries|TestMediaRequestAuthenticationBeforeJSON|TestMediaRequestKeysAndPagination|TestMediaRequestsNoStoreIncludesEveryOutcome|TestMediaRequestSafeErrors|TestMediaRequestApprovalPublicSubscriptionFormat'
 export PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE=1 PULSE_M5_AUTOMATIC_TV_ACCEPTANCE=1
 export PULSE_M9_REQUEST_GROUP_WORK="$work"
 bash scripts/with_podman_test_postgres.sh bash -c '
  set -euo pipefail
  for group in STORE PUBLIC; do
   if [[ "$group" == STORE ]]; then pkg=./internal/store/postgres; expected=14; pattern="^TestMediaRequest(Submission|Close|Read|Submit|Validation|Schema107|Approval).*PG$"; else pkg=./internal/httpapi; expected=3; pattern="^TestM9MediaRequest(CookieHTTP|AutomaticTVNativeHTTP|StoreFailureHTTP)PG$"; fi
   log="$PULSE_M9_REQUEST_GROUP_WORK/$group.log"
   if ! go test -race "$pkg" -count=1 -timeout=8m -run "$pattern" -v >"$log" 2>&1; then tail -n 170 "$log"; exit 1; fi
   count=$(grep -c "^--- PASS: Test" "$log" || true)
   if [[ "$count" != "$expected" ]] || grep -q -- "--- SKIP:" "$log"; then tail -n 170 "$log"; echo "unexpected $group parent count=$count" >&2; exit 1; fi
   grep -E "^--- PASS:|M9_MEDIA_REQUEST|M9_REQUEST" "$log" || true
  done
 '
 node --test --test-reporter=tap web/tests/media-requests.test.mjs >"$work/frontend.log" 2>&1 || { tail -n 130 "$work/frontend.log"; exit 1; }
 for receipt in '# tests 25' '# pass 25' '# fail 0' '# skipped 0'; do grep -Fxq "$receipt" "$work/frontend.log" || { cat "$work/frontend.log"; exit 1; }; done
 echo M9_MEDIA_REQUESTS_BACKEND_ACCEPTANCE_PASS_parents=34_skipped=0_frontend=25
fi
if [[ "$mode" != backend ]]; then
 PULSE_M9_REQUEST_BROWSER=1 bash scripts/verify_m9_media_requests_browser.sh
fi
echo M9_MEDIA_REQUESTS_FORMAL_ACCEPTANCE_PASS_mode="$mode"
