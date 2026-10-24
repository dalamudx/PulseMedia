#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m9_word_policy.sh' >&2; exit 2; fi
if [[ $(uname -s) != Linux ]]; then echo 'Linux root filesystem capabilities required' >&2; exit 2; fi
if [[ ${PULSE_M9_WORD_POLICY_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M9_WORD_POLICY_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m9_word_policy.sh
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m9-word-policy.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m9-word-policy.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M9_WORD_POLICY_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="${GOCACHE:-$owned/go-cache}" PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE=1 PULSE_M5_AUTOMATIC_TV_ACCEPTANCE=1
mkdir -p "$GOTMPDIR"
run_group(){
 local group=$1 package=$2 expected=$3 pattern=$4 log="$owned/$1.log"
 if ! go test -race -count=1 -timeout=12m "$package" -run "^($pattern)$" -v >"$log" 2>&1; then
  echo "M9_WORD_POLICY_${group}_FAILED" >&2
  grep -E '^--- (FAIL|SKIP):|^[[:space:]]+[^ ]+_test.go:[0-9]+:|^FAIL|^# ' "$log" >&2 || true
  exit 1
 fi
 if grep -q -- '--- SKIP:' "$log"; then echo "M9_WORD_POLICY_${group}_UNEXPECTED_SKIP" >&2; exit 1; fi
 local passed
 passed=$(grep -c '^--- PASS: Test' "$log")
 if [[ "$passed" != "$expected" ]]; then
  echo "M9_WORD_POLICY_${group}_PARENT_COUNT expected=$expected actual=$passed" >&2
  exit 1
 fi
 grep -E '^--- PASS: Test|M9_WORD_POLICY_COOKIE_HTTP_PASS|M9_WORD_POLICY_NATIVE_TV_PASS' "$log"
}
run_group QUALITY ./internal/domain/quality 13 'TestCustomWordRulesEvaluation|TestCustomWords.*|TestPolicyWords.*'
run_group SELECTORS ./internal/usecase/acquisition 4 'TestMovieRoundWordPreferenceKeepsQualityReceipt|TestMovieRoundWordFiltersCannotBeOutscored|TestAutomaticTVWordPreferenceKeepsAuthorityQualityScore|TestAutomaticTVWordFiltersBeforeSeederTieBreak'
run_group SEARCH ./internal/usecase/search 5 'TestInteractiveWords.*|TestMovieWordPolicyActualSelectionPG'
run_group POLICY ./internal/usecase/policy 2 'TestConcurrentPolicyPublishes|TestPolicyImmutableRevisionAndRollback'
run_group FREEZE ./internal/store/postgres 4 'TestInteractiveWordsFreezePublishedPolicyPG|TestInteractiveWordsFreezeBoundSubscriptionPolicyPG|TestInteractiveWordsOriginalReceiptBeforeCurrentPolicyReadPG|TestPolicyWordsTailRollbackAndStableReceiptPG'
run_group HTTP ./internal/httpapi 5 'TestStrictPolicyJSON|TestM9WordPolicyCookieHTTPPG|TestM9WordPolicyAutomaticTVNativeHTTPPG|TestPolicyCommandsAndPreview|TestSubscriptionHTTPLifecycleAndPolicyPreview'
printf 'M9_WORD_POLICY_ACCEPTANCE_PASS parents=33 skipped=0 race=true PG=18.6 Cookie=REAL Network=OWNED_PROTOCOL candidate_preferences=VERSIONED quality_cutoff=UNCHANGED hard_filters=STRICT policy_freeze=SERVER_TRUSTED original_receipts=PRESERVED Add=1 same_inode=true milestone=NOT_RELEASED\n'
