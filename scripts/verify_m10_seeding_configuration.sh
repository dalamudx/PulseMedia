#!/usr/bin/env bash
set -euo pipefail
work=$(mktemp -d /data/temp/pulse-m10-seeding-config.XXXXXX)
cleanup() {
 local result=$?
 rm -rf -- "$work"
 if [[ -e "$work" ]]; then printf 'Seeding configuration cleanup failed: %s\n' "$work" >&2; exit 1; fi
 printf 'M10_SEEDING_CONFIG_FORMAL_TEMP_CLEANUP_VERIFIED\n'
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
 grep -E '^--- PASS:|M10_SEEDING_' "$work/$label.log" || true
 printf 'M10_SEEDING_CONFIG_GROUP_PASS label=%s parents=%s skipped=0\n' "$label" "$count"
}
run_group DOMAIN ./internal/domain/seeding 5 '^TestSeeding'
run_group QBIT_NATIVE ./internal/adapter/downloader/qbittorrent 7 '^TestSeeding(Native|Space)'
run_group TRANSMISSION_NATIVE ./internal/adapter/downloader/transmission 7 '^TestSeeding(Native|Space)'
run_group USECASE ./internal/usecase/seeding 5 '^TestSeedingConfig'
run_group HTTP ./internal/httpapi 5 '^TestSeedingConfiguration'
export PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE=1 PULSE_M10_SEED_CONFIG_FORMAL_WORK="$work"
bash scripts/with_podman_test_postgres.sh bash -c '
 set -euo pipefail
 run_group() {
  local label=$1 package=$2 expected=$3 pattern=$4 count
  local log="$PULSE_M10_SEED_CONFIG_FORMAL_WORK/$label.log"
  if ! go test -race "$package" -count=1 -timeout=8m -run "$pattern" -v > "$log" 2>&1; then tail -n 150 "$log"; return 1; fi
  count=$(grep -c "^--- PASS: Test" "$log" || true)
  if [[ "$count" != "$expected" ]] || grep -q -- "--- SKIP:" "$log"; then tail -n 150 "$log"; printf "Unexpected %s count=%s expected=%s or skipped case\n" "$label" "$count" "$expected" >&2; return 1; fi
  grep -E "^--- PASS:|M10_SEEDING_" "$log" || true
  printf "M10_SEEDING_CONFIG_GROUP_PASS label=%s parents=%s skipped=0\n" "$label" "$count"
 }
 run_group STORE ./internal/store/postgres 7 "^TestSeedingConfig.*PG$"
 run_group PUBLIC ./internal/httpapi 2 "^TestM10SeedingConfig(Cookie|StorageFailure)HTTPPG$"
'
printf 'M10_SEEDING_CONFIG_FORMAL_PASS parents=38 skipped=0 PG=18.6 race=true automatic_removal=NOT_ACCEPTED\n'
