#!/usr/bin/env bash
# Owned acceptance for the live system log viewer (M7-06).
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-all}
if [[ $# -gt 1 || ( $mode != all && $mode != check ) ]]; then echo 'Usage: verify_m7_live_logs.sh [all|check]' >&2; exit 2; fi
if [[ ${PULSE_M7_LIVE_LOGS_DEADLINE:-0} != 1 ]]; then
 exec timeout --signal=TERM --kill-after=20s 900s env PULSE_M7_LIVE_LOGS_DEADLINE=1 mise exec -- bash scripts/verify_m7_live_logs.sh "$mode"
fi
if [[ $mode == all && ${PULSE_M7_LIVE_LOGS_OWNED_PG:-0} != 1 ]]; then
 exec env PULSE_M7_LIVE_LOGS_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m7_live_logs.sh "$mode"
fi
owned=$(mktemp -d /data/temp/pulse-m7-live-logs.XXXXXX)
cleanup(){
 rc=$?
 trap - EXIT
 case "$owned" in /data/temp/pulse-m7-live-logs.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M7_LIVE_LOGS_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache"
export XDG_CACHE_HOME="$owned/cache" XDG_CONFIG_HOME="$owned/config"
export PULSE_M7_LIVE_LOGS_REPO="$repo" PULSE_M7_LIVE_LOGS_ASSETS="$owned/web" PULSE_M7_LIVE_LOGS_SCREENSHOTS="$owned/screenshots"
mkdir -p "$GOTMPDIR" "$PULSE_M7_LIVE_LOGS_SCREENSHOTS"
files=(
 ./internal/httpapi/m5_indexer_acceptance_test.go
 ./internal/httpapi/m5_interactive_search_acceptance_test.go
 ./internal/httpapi/m5_subscription_creation_acceptance_test.go
 ./internal/httpapi/file_mapping_browser_helper_test.go
 ./internal/httpapi/m7_live_logs_browser_test.go
)
bash -n scripts/verify_m7_live_logs.sh
node --check web/tests/m7-live-logs-browser.mjs
cat > "$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const env=process.env,load=createRequire(env.PULSE_M7_LIVE_LOGS_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
export default {root:env.PULSE_M7_LIVE_LOGS_REPO+'/web',envDir:env.TMPDIR,plugins:[react()],cacheDir:env.TMPDIR+'/vite-cache',resolve:{alias:{'@':env.PULSE_M7_LIVE_LOGS_REPO+'/web/src'}},build:{outDir:env.PULSE_M7_LIVE_LOGS_ASSETS,emptyOutDir:true}};
CONFIG
node --check "$owned/vite.config.mjs"
if [[ $mode == check ]]; then
 mise exec -- go test -race -c -o "$owned/live-logs.test" "${files[@]}"
 test -s "$owned/live-logs.test"
 echo M7_LIVE_LOGS_RUNTIME_READY
 exit 0
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
export PULSE_M7_LIVE_LOGS_BROWSER=1
export PULSE_M7_LIVE_LOGS_CHROME_BIN="${PULSE_M7_LIVE_LOGS_CHROME_BIN:-$(command -v google-chrome || command -v chromium || true)}"
test -x "$PULSE_M7_LIVE_LOGS_CHROME_BIN"
mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/web-build.log" 2>&1 || { tail -n 30 "$owned/web-build.log"; exit 1; }
mise exec -- go test -race "${files[@]}" -run '^TestM7LiveLogsActualChromeHTTPPG$' -count=1 -v -timeout=240s 2>&1 | tee "$owned/acceptance.log"
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M7_LIVE_LOGS_UNEXPECTED_SKIP >&2; exit 1; fi
test "$(grep -c '^--- PASS: TestM7LiveLogsActualChromeHTTPPG (' "$owned/acceptance.log")" = 1
for marker in M7_LIVE_LOGS_BROWSER_PASS M7_LIVE_LOGS_APP_COOKIE_PG_PASS M7_LIVE_LOGS_CHROME_CLEANUP_VERIFIED; do
 test "$(grep -c "$marker" "$owned/acceptance.log")" = 1
done
for width in 1280 375; do
 test -s "$PULSE_M7_LIVE_LOGS_SCREENSHOTS/live-logs-$width.png"
done
test -s "$PULSE_M7_LIVE_LOGS_SCREENSHOTS/live-logs-empty-375.png"
printf 'M7_LIVE_LOGS_SCREENSHOTS_READY %s\n' "$PULSE_M7_LIVE_LOGS_SCREENSHOTS"
# Review happens after this run: the runner copies the images to a caller-owned
# directory so the owned temp tree can still be deleted immediately.
if [[ -n "${PULSE_M7_LIVE_LOGS_REVIEW_DIR:-}" ]]; then
 test ! -L "$PULSE_M7_LIVE_LOGS_REVIEW_DIR"
 mkdir -p "$PULSE_M7_LIVE_LOGS_REVIEW_DIR"
 cp -- "$PULSE_M7_LIVE_LOGS_SCREENSHOTS"/*.png "$PULSE_M7_LIVE_LOGS_REVIEW_DIR"/
 printf 'M7_LIVE_LOGS_REVIEW_COPY %s\n' "$PULSE_M7_LIVE_LOGS_REVIEW_DIR"
fi
echo 'M7_LIVE_LOGS_ACCEPTANCE_EXECUTED tests=1 skipped=0 redacted=true level_filter=true paused=true writes=0'
