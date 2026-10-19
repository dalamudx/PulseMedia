#!/usr/bin/env bash
# mise exec -- bash scripts/verify_m5_episode_tracking.sh [all|states|recovery]
# Actual DOM assertions, with optional task-owned screenshots before cleanup.
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [ "$#" -gt 1 ]; then echo 'Usage: verify_m5_episode_tracking.sh [all|states|recovery]' >&2; exit 2; fi
mode=${1:-all}
case "$mode" in
 all) pattern='^TestM5EpisodeTracking(States|Recovery)ActualChromeHTTPPG$'; expected=2; receipts='M5_EPISODE_STATES_PASS M5_EPISODE_RECOVERY_PASS';;
 states) pattern='^TestM5EpisodeTrackingStatesActualChromeHTTPPG$'; expected=1; receipts=M5_EPISODE_STATES_PASS;;
 recovery) pattern='^TestM5EpisodeTrackingRecoveryActualChromeHTTPPG$'; expected=1; receipts=M5_EPISODE_RECOVERY_PASS;;
 *) echo 'Usage: verify_m5_episode_tracking.sh [all|states|recovery]' >&2; exit 2;;
esac
if [ "${PULSE_M5_EPISODE_OWNED_PG:-}" != 1 ]; then
 exec env PULSE_M5_EPISODE_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m5_episode_tracking.sh "$mode"
fi
: "${PULSE_TEST_DATABASE_URL:?private PostgreSQL fixture required}"
owned=$(mktemp -d /data/temp/pulse-m5-episode.XXXXXX)
cleanup(){
 rc=$?
 trap - EXIT
 case "$owned" in /data/temp/pulse-m5-episode.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M5_EPISODE_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build"
export PULSE_M5_CREATION_REPO="$repo" PULSE_M5_CREATION_ASSETS="$owned/built-web"
export PULSE_M5_INDEXER_ACCEPTANCE=1 PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE=1 PULSE_M5_EPISODE_BROWSER=1
mkdir -p "$GOTMPDIR"
cat > "$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const load=createRequire(process.env.PULSE_M5_CREATION_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
export default {
 root:process.env.PULSE_M5_CREATION_REPO+'/web',envDir:process.env.TMPDIR,plugins:[react()],
 resolve:{alias:{'@':process.env.PULSE_M5_CREATION_REPO+'/web/src'}},
 build:{outDir:process.env.PULSE_M5_CREATION_ASSETS,emptyOutDir:true}
};
CONFIG
mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/web-build.log" 2>&1 || { tail -n 30 "$owned/web-build.log"; exit 1; }
files=(
 ./internal/httpapi/m5_indexer_acceptance_test.go
 ./internal/httpapi/m5_interactive_search_acceptance_test.go
 ./internal/httpapi/m5_subscription_creation_acceptance_test.go
 ./internal/httpapi/m5_subscription_creation_browser_test.go
 ./internal/httpapi/m5_episode_tracking_browser_test.go
)
if ! mise exec -- go test -race "${files[@]}" -run "$pattern" -count=1 -v -timeout=7m >"$owned/acceptance.log" 2>&1; then
 tail -n 100 "$owned/acceptance.log"; exit 1
fi
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M5_EPISODE_UNEXPECTED_SKIP >&2; exit 1; fi
actual=$(grep -c '^--- PASS: TestM5EpisodeTracking' "$owned/acceptance.log")
if [ "$actual" != "$expected" ]; then echo "M5_EPISODE_MISSING_EXECUTION expected=$expected actual=$actual" >&2; exit 1; fi
for receipt in $receipts; do grep -q "$receipt" "$owned/acceptance.log" || exit 1; done
grep -E 'M5_EPISODE_|--- PASS:|^ok[[:space:]]' "$owned/acceptance.log"
echo "M5_EPISODE_ACCEPTANCE_EXECUTED mode=$mode tests=$actual"
