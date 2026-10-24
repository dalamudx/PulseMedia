#!/usr/bin/env bash
set -euo pipefail
work=$(mktemp -d /data/temp/pulse-m10-seeding-management.XXXXXX)
cleanup() {
 local result=$?
 rm -rf -- "$work"
 if [[ -e "$work" ]]; then printf 'Seeding management cleanup failed: %s\n' "$work" >&2; exit 1; fi
 printf 'M10_SEEDING_MANAGEMENT_TEMP_CLEANUP_VERIFIED\n'
 exit "$result"
}
trap cleanup EXIT
export TMPDIR="$work" TMP="$work" TEMP="$work" GOTMPDIR="$work/build"
mkdir -p "$GOTMPDIR"
run_group() {
 local label=$1 package=$2 expected=$3 pattern=$4 count
 local log="$work/$label.log"
 if ! go test -race "$package" -count=1 -timeout=12m -run "$pattern" -v > "$log" 2>&1; then tail -n 160 "$log"; return 1; fi
 count=$(grep -c '^--- PASS: Test' "$log" || true)
 if [[ "$count" != "$expected" ]] || grep -q -- '--- SKIP:' "$log"; then tail -n 160 "$log"; printf 'Unexpected %s count=%s expected=%s or skipped case\n' "$label" "$count" "$expected" >&2; return 1; fi
 grep -E '^--- PASS:|M10_SEEDING_' "$log" || true
 printf 'M10_SEEDING_MANAGEMENT_GROUP_PASS label=%s parents=%s skipped=0\n' "$label" "$count"
}
run_group DOMAIN ./internal/domain/seeding 5 '^TestSeeding'
run_group QBIT_NATIVE ./internal/adapter/downloader/qbittorrent 15 '^TestSeeding'
run_group TRANSMISSION_NATIVE ./internal/adapter/downloader/transmission 16 '^Test(Seeding|TorrentSingleFileContentPath)'
run_group USECASE ./internal/usecase/seeding 13 '^TestSeeding'
run_group HTTP ./internal/httpapi 5 '^TestSeedingConfiguration'
export PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE=1 PULSE_M10_SEEDING_FORMAL_WORK="$work"
export -f run_group
bash scripts/with_podman_test_postgres.sh bash -c '
 set -euo pipefail
 work="$PULSE_M10_SEEDING_FORMAL_WORK"
 run_group STORE ./internal/store/postgres 15 "^Test(Seeding(Config.*|AutomaticTVAuthorityGuard|CompleteTVAuthorityGuard|UpgradeAuthorityGuard)|DownloadTaskBinding.*)PG$"
 run_group PUBLIC ./internal/httpapi 2 "^TestM10SeedingConfig(Cookie|StorageFailure)HTTPPG$"
 run_group APP ./internal/app 18 "^Test(M10(OrdinarySeedingProtectionNative|Seeding.*)PG|SeedingFactory.*)$"
'
printf 'M10_SEEDING_MANAGEMENT_FORMAL_PASS parents=89 skipped=0 PG=18.6 race=true retain_files=MANDATORY historical_and_specialized=RETAINED UI=M10_07\n'
