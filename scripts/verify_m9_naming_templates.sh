#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m9_naming_templates.sh' >&2; exit 2; fi
if [[ $(uname -s) != Linux ]]; then echo 'Linux root filesystem capabilities required' >&2; exit 2; fi
if [[ ${PULSE_M9_NAMING_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M9_NAMING_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m9_naming_templates.sh
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m9-naming.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m9-naming.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M9_NAMING_TEMPLATES_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="${GOCACHE:-$owned/go-cache}" PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE=1 PULSE_M8_UNTRACKED_ORGANIZATION_ACCEPTANCE=1
mkdir -p "$GOTMPDIR"
run_group(){
 local group=$1 package=$2 expected=$3 pattern=$4 log="$owned/$1.log"
 if ! go test -race -count=1 -timeout=12m "$package" -run "^($pattern)$" -v >"$log" 2>&1; then
  echo "M9_NAMING_${group}_FAILED" >&2
  grep -E '^--- (FAIL|SKIP):|^[[:space:]]+[^ ]+_test.go:[0-9]+:|^FAIL|^# ' "$log" >&2 || true
  exit 1
 fi
 if grep -q -- '--- SKIP:' "$log"; then echo "M9_NAMING_${group}_UNEXPECTED_SKIP" >&2; exit 1; fi
 local passed
 passed=$(grep -c '^--- PASS: Test' "$log")
 if [[ "$passed" != "$expected" ]]; then echo "M9_NAMING_${group}_PARENT_COUNT expected=$expected actual=$passed" >&2; exit 1; fi
 grep -E '^--- PASS: Test|M9_NAMING_COOKIE_HTTP_PASS|M8_UNTRACKED_ORGANIZATION_APP_PASS' "$log"
}
run_group DOMAIN ./internal/domain/naming 23 'Test.*'
run_group SETTINGS ./internal/usecase/settings 6 'TestNamingPreview.*PG|TestNamingTemplateSettings.*PG'
run_group HTTP ./internal/httpapi 6 'TestStrictNamingJSON|TestNamingNoStoreIncludesErrors|TestM9NamingPreviewCookieHTTPPG|TestCompleteTVNamingVersionStrictJSON|TestM9CompleteTVNamingPreviewVersionAndFrozenRecoveryPG|TestM9CompleteTVNamingInvalidTargetKeepsDemandPG'
run_group PLANS ./internal/store/transfer 11 'TestNamingConfiguration.*PG|TestAutomaticTVNaming.*PG|TestLibraryReuseNamingPreservesExplicitTargetPG|TestTransferHardlinkExecutionAndObservationRecovery|TestAutomaticTVOrganizationNativeAtomicPG|TestLibraryReuseExecutionPG'
run_group UPGRADE ./internal/store/postgres 7 'TestMovieUpgradeNaming.*PG|TestMovieUpgradeOrganizationExecutionNativePG|TestMovieUpgradeOrganizationIngestionPG|TestMovieUpgradeOrganizationJobCommandPG'
run_group EXPLICIT ./internal/app 1 'TestM9NamingPreservesExplicitExternalOrganizationRuntimePG'
node --test web/tests/complete-tv-organization-admission.test.mjs >"$owned/journal.log" 2>&1 || { cat "$owned/journal.log" >&2; exit 1; }
if ! grep -Eq '^ℹ tests 15$|^# tests 15$' "$owned/journal.log" || ! grep -Eq '^ℹ skipped 0$|^# skipped 0$' "$owned/journal.log" || ! grep -Eq '^ℹ fail 0$|^# fail 0$' "$owned/journal.log"; then cat "$owned/journal.log" >&2; exit 1; fi
printf 'M9_NAMING_TEMPLATES_ACCEPTANCE_PASS parents=54 skipped=0 journal_tests=15 race=true PG=18.6 Cookie=REAL paths=FIRST_PLAN_FROZEN preview=READ_ONLY hardlink=REAL source_container=PRESERVED explicit_targets=PRESERVED recovery=OBSERVE_ONLY milestone=NOT_RELEASED\n'
