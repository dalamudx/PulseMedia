#!/usr/bin/env bash
set -euo pipefail
work=$(mktemp -d /data/temp/pulse-m10-bangumi-calendar.XXXXXX)
cleanup() {
 local result=$?
 rm -rf -- "$work"
 if [[ -e "$work" ]]; then printf 'Bangumi calendar cleanup failed: %s\n' "$work" >&2; exit 1; fi
 printf 'M10_BANGUMI_FORMAL_TEMP_CLEANUP_VERIFIED\n'
 exit "$result"
}
trap cleanup EXIT
export TMPDIR="$work" TMP="$work" TEMP="$work" GOTMPDIR="$work/build"
mkdir -p "$GOTMPDIR"
run_group() {
 local label=$1 package=$2 expected=$3 pattern=$4 count
 if ! go test -race "$package" -count=1 -timeout=8m -run "$pattern" -v > "$work/$label.log" 2>&1; then tail -n 130 "$work/$label.log"; return 1; fi
 count=$(grep -c '^--- PASS: Test' "$work/$label.log" || true)
 if [[ "$count" != "$expected" ]] || grep -q -- '--- SKIP:' "$work/$label.log"; then tail -n 130 "$work/$label.log"; printf 'Unexpected %s count=%s expected=%s or skipped case\n' "$label" "$count" "$expected" >&2; return 1; fi
 grep -E '^--- PASS:|M10_BANGUMI_' "$work/$label.log" || true
 printf 'M10_BANGUMI_GROUP_PASS label=%s parents=%s skipped=0\n' "$label" "$count"
}
mode=${1:-all}
if [[ "$mode" == live ]]; then
 export PULSE_BANGUMI_LIVE=1
 run_group LIVE ./internal/adapter/metadata/bangumi 1 '^TestLiveBangumiCalendar$'
 printf 'M10_BANGUMI_FORMAL_LIVE_PASS requests=1 retries=0 TLS=VERIFIED\n'
 exit 0
fi
if [[ "$mode" != all ]]; then printf 'Expected all or live\n' >&2; exit 1; fi
run_group NATIVE ./internal/adapter/metadata/bangumi 7 '^Test(GetCalendar|Bangumi)'
run_group USECASE ./internal/usecase/calendar 5 '^TestCalendar'
export PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE=1 PULSE_M10_CALENDAR_FORMAL_WORK="$work"
bash scripts/with_podman_test_postgres.sh bash -c '
 set -euo pipefail
 log="$PULSE_M10_CALENDAR_FORMAL_WORK/PUBLIC.log"
 if ! go test -race ./internal/httpapi ./internal/app -count=1 -timeout=10m -run "^Test(M10BangumiCalendar(Cookie|ErrorsAndEmpty)HTTPPG|M5SubscriptionCreationNativeSparseRecoveryHTTPPG|FullM10EcosystemIntegration)$" -v > "$log" 2>&1; then tail -n 150 "$log"; exit 1; fi
 count=$(grep -c "^--- PASS: Test" "$log" || true)
 if [[ "$count" != 4 ]] || grep -q -- "--- SKIP:" "$log"; then tail -n 150 "$log"; printf "Unexpected public/compat count=%s or skipped case\n" "$count" >&2; exit 1; fi
 grep -E "^--- PASS:|M10_BANGUMI_" "$log" || true
 printf "M10_BANGUMI_GROUP_PASS label=PUBLIC parents=2 compatibility_parents=2 skipped=0\n"
'
printf 'M10_BANGUMI_FORMAL_PASS parents=14 compatibility_parents=2 skipped=0 PG=18.6 race=true UI=M10_06\n'
