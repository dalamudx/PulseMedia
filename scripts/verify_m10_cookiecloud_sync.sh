#!/usr/bin/env bash
set -euo pipefail
m10_sync_work=$(mktemp -d /data/temp/pulse-m10-cookiecloud-sync.XXXXXX)
cleanup() {
 local result=$?
 rm -rf -- "$m10_sync_work"
 if [[ -e "$m10_sync_work" ]]; then printf 'CookieCloud synchronization cleanup failed: %s\n' "$m10_sync_work" >&2; exit 1; fi
 printf 'M10_COOKIECLOUD_FORMAL_SYNC_TEMP_CLEANUP_VERIFIED\n'
 exit "$result"
}
trap cleanup EXIT
export TMPDIR="$m10_sync_work" TMP="$m10_sync_work" TEMP="$m10_sync_work" GOTMPDIR="$m10_sync_work/build"
export PULSE_M10_SYNC_FORMAL_WORK="$m10_sync_work" PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE=1
mkdir -p "$GOTMPDIR"
run_group() {
 local label=$1 package=$2 expected=$3 pattern=$4 count
 local log="$PULSE_M10_SYNC_FORMAL_WORK/$label.log"
 if ! go test -race "$package" -count=1 -timeout=12m -run "$pattern" -v > "$log" 2>&1; then tail -n 150 "$log"; return 1; fi
 count=$(grep -c '^--- PASS: Test' "$log" || true)
 if [[ "$count" != "$expected" ]] || grep -q -- '--- SKIP:' "$log"; then tail -n 150 "$log"; printf 'Unexpected %s parents=%s expected=%s or skipped case\n' "$label" "$count" "$expected" >&2; return 1; fi
 grep -E '^--- PASS:|M10_COOKIECLOUD_.*PASS' "$log" || true
 printf 'M10_COOKIECLOUD_SYNC_GROUP_PASS label=%s parents=%s skipped=0\n' "$label" "$count"
}
export -f run_group
run_group DOMAIN ./internal/domain/cookies 5 '^TestCookie'
run_group NATIVE ./internal/adapter/cookiecloud 7 '^TestCookieCloud'
run_group USECASE ./internal/usecase/cookiecloud 10 '^TestCookieCloud'
run_group HTTP ./internal/httpapi 5 '^TestCookieCloudConfiguration'
bash scripts/with_podman_test_postgres.sh bash -c '
 set -euo pipefail
 run_group CONFIG_STORE ./internal/store/postgres 7 "^TestCookieCloudConfig(ConcurrentOriginal|AccountHistoricalAndOffline|ConcurrentVersions|TailRollback|DeferredRevocation|Schema108Roundtrip|NoExecutionAndCancellation)PG$"
 run_group PUBLIC ./internal/httpapi 2 "^TestM10CookieCloudConfig(Cookie|StorageFailure)HTTPPG$"
 run_group SCHEDULE ./internal/store/postgres 5 "^TestCookieCloudSync.*PG$"
 run_group PERMIT_CURRENT ./internal/store/postgres 7 "^TestCookieCloudPermit.*PG$"
 run_group CREDENTIAL_EVIDENCE ./internal/store/postgres 4 "^TestCookieCloudCredential.*PG$"
 run_group APP_NATIVE ./internal/app 4 "^TestM10CookieCloud(NativeSyncAndSourceUse|DisabledLateResponse|ShutdownWaitsForRead|WritebackFailureDoesNotReplay)PG$"
 run_group COMPAT_STORE ./internal/store/postgres 4 "^Test(IndexerConfiguration(Conversion|Rollback|HistoricalAndRoundtrip)|IndexerManagementConfigurationValidation)PG$"
 run_group COMPAT_APP ./internal/app 9 "^Test(FullM10EcosystemIntegration|M8SiteAccountStatsRuntime(Positive|Failure|DisableLate)PG|M8SiteCheckinRuntime(Positive|LostReceipt|PreparedRestart|DisableLate)PG|SiteAccountStatsPhysicalWaitsForExecution)$"
'
printf 'M10_COOKIECLOUD_FULL_SYNC_FORMAL_PASS parents=56 compatibility_parents=13 skipped=0 PG=18.6 race=true native_App=true external_private_PT=NOT_TESTED\n'
