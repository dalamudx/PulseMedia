#!/usr/bin/env bash
# Configuration and read-side foundation only; this is not full site-sync acceptance.
set -euo pipefail
work=$(mktemp -d /data/temp/pulse-m10-cookiecloud-config.XXXXXX)
cleanup() {
 local result=$?
 rm -rf -- "$work"
 if [[ -e "$work" ]]; then printf 'CookieCloud configuration cleanup failed: %s\n' "$work" >&2; exit 1; fi
 printf 'M10_COOKIECLOUD_FORMAL_CONFIG_TEMP_CLEANUP_VERIFIED\n'
 exit "$result"
}
trap cleanup EXIT
export TMPDIR="$work" TMP="$work" TEMP="$work" GOTMPDIR="$work/build"
mkdir -p "$GOTMPDIR"
run_group() {
 local label=$1 package=$2 expected=$3 pattern=$4 count
 if ! go test -race "$package" -count=1 -timeout=8m -run "$pattern" -v > "$work/$label.log" 2>&1; then tail -n 130 "$work/$label.log"; return 1; fi
 count=$(grep -c '^--- PASS: Test' "$work/$label.log" || true)
 if [[ "$count" != "$expected" ]] || grep -q -- '--- SKIP:' "$work/$label.log"; then tail -n 130 "$work/$label.log"; printf 'Unexpected %s parents=%s expected=%s or skipped case\n' "$label" "$count" "$expected" >&2; return 1; fi
 printf 'M10_COOKIECLOUD_CONFIG_GROUP_PASS label=%s parents=%s skipped=0\n' "$label" "$count"
}
run_group DOMAIN ./internal/domain/cookies 5 '^TestCookie'
run_group NATIVE ./internal/adapter/cookiecloud 7 '^TestCookieCloud'
run_group USECASE ./internal/usecase/cookiecloud 10 '^TestCookieCloud'
run_group HTTP ./internal/httpapi 5 '^TestCookieCloudConfiguration'
export PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE=1 PULSE_M10_CONFIG_FORMAL_WORK="$work"
bash scripts/with_podman_test_postgres.sh bash -c '
 set -euo pipefail
 for group in STORE PUBLIC; do
  if [[ "$group" == STORE ]]; then
   package=./internal/store/postgres; expected=7; pattern="^TestCookieCloudConfig(ConcurrentOriginal|AccountHistoricalAndOffline|ConcurrentVersions|TailRollback|DeferredRevocation|Schema108Roundtrip|NoExecutionAndCancellation)PG$"
  else
   package=./internal/httpapi; expected=2; pattern="^TestM10CookieCloudConfig(Cookie|StorageFailure)HTTPPG$"
  fi
  log="$PULSE_M10_CONFIG_FORMAL_WORK/$group.log"
  if ! go test -race "$package" -count=1 -timeout=10m -run "$pattern" -v > "$log" 2>&1; then tail -n 150 "$log"; exit 1; fi
  count=$(grep -c "^--- PASS: Test" "$log" || true)
  if [[ "$count" != "$expected" ]] || grep -q -- "--- SKIP:" "$log"; then tail -n 150 "$log"; printf "Unexpected %s count=%s expected=%s or skipped case\n" "$group" "$count" "$expected" >&2; exit 1; fi
  grep -E "^--- PASS:|M10_COOKIECLOUD_CONFIG_" "$log" || true
  printf "M10_COOKIECLOUD_CONFIG_GROUP_PASS label=%s parents=%s skipped=0\n" "$group" "$count"
 done
'
printf 'M10_COOKIECLOUD_CONFIGURATION_FORMAL_PASS parents=36 skipped=0 full_site_sync=NOT_ACCEPTED\n'
