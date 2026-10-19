#!/usr/bin/env bash
# Isolated development acceptance: check compiles only; all is owned by Main.
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-all}
if [[ $# -gt 1 || ( $mode != all && $mode != check ) ]]; then echo 'Usage: verify_m6_compact_controls_ui.sh [all|check]' >&2; exit 2; fi
if [[ ${PULSE_M6_COMPACT_CONTROLS_DEADLINE:-0} != 1 ]]; then
 exec timeout --signal=TERM --kill-after=20s 600s env PULSE_M6_COMPACT_CONTROLS_DEADLINE=1 mise exec -- bash scripts/verify_m6_compact_controls_ui.sh "$mode"
fi
if [[ $mode == all && ${PULSE_M6_COMPACT_CONTROLS_OWNED_PG:-0} != 1 ]]; then
 exec env PULSE_M6_COMPACT_CONTROLS_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m6_compact_controls_ui.sh "$mode"
fi
owned=$(mktemp -d /data/temp/pulse-m6-compact-controls.XXXXXX)
cleanup(){
 rc=$?
 trap - EXIT
 case "$owned" in /data/temp/pulse-m6-compact-controls.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M6_COMPACT_CONTROLS_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache"
export XDG_CACHE_HOME="$owned/cache" XDG_CONFIG_HOME="$owned/config"
export PULSE_M6_COMPACT_CONTROLS_REPO="$repo" PULSE_M6_COMPACT_CONTROLS_ASSETS="$owned/web" PULSE_M6_COMPACT_CONTROLS_SCREENSHOTS="$owned/screenshots"
mkdir -p "$GOTMPDIR" "$PULSE_M6_COMPACT_CONTROLS_SCREENSHOTS"
files=(
 ./internal/httpapi/m5_indexer_acceptance_test.go
 ./internal/httpapi/m5_interactive_search_acceptance_test.go
 ./internal/httpapi/m5_subscription_creation_acceptance_test.go
 ./internal/httpapi/file_mapping_browser_helper_test.go
 ./internal/httpapi/m6_compact_controls_browser_test.go
)
bash -n scripts/verify_m6_compact_controls_ui.sh
node --check web/tests/m6-compact-controls-browser.mjs
cat > "$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const env=process.env,load=createRequire(env.PULSE_M6_COMPACT_CONTROLS_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
export default {root:env.PULSE_M6_COMPACT_CONTROLS_REPO+'/web',envDir:env.TMPDIR,plugins:[react()],cacheDir:env.TMPDIR+'/vite-cache',resolve:{alias:{'@':env.PULSE_M6_COMPACT_CONTROLS_REPO+'/web/src'}},build:{outDir:env.PULSE_M6_COMPACT_CONTROLS_ASSETS,emptyOutDir:true}};
CONFIG
node --check "$owned/vite.config.mjs"
if [[ $mode == check ]]; then
 mise exec -- go test -race -c -o "$owned/compact-controls.test" "${files[@]}"
 test -s "$owned/compact-controls.test"
 echo M6_COMPACT_CONTROLS_RUNTIME_READY
 exit 0
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
export PULSE_M6_COMPACT_CONTROLS_BROWSER=1
mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/web-build.log" 2>&1 || { echo M6_COMPACT_CONTROLS_VITE_BUILD_FAILED >&2; exit 1; }
mise exec -- go test -race "${files[@]}" -run '^TestM6CompactControlsActualChromeHTTPPG$' -count=1 -v -timeout=240s 2>&1 | tee "$owned/acceptance.log"
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M6_COMPACT_CONTROLS_UNEXPECTED_SKIP >&2; exit 1; fi
test "$(grep -c '^--- PASS: TestM6CompactControlsActualChromeHTTPPG (' "$owned/acceptance.log")" = 1
test "$(grep -c '^--- PASS:' "$owned/acceptance.log")" = 1
for marker in M6_COMPACT_CONTROLS_BROWSER_PASS M6_COMPACT_CONTROLS_APP_COOKIE_PG_PASS M6_COMPACT_CONTROLS_CHROME_CLEANUP_VERIFIED; do
 test "$(grep -c "$marker" "$owned/acceptance.log")" = 1
done
for width in 1280 768 375 320; do
 for state in token-secret token-create-pending token-revoke-control token-revoke-confirm token-revoke-pending policy-pending policy-confirmed; do
  test -s "$PULSE_M6_COMPACT_CONTROLS_SCREENSHOTS/$state-$width.png"
 done
done
test -s "$PULSE_M6_COMPACT_CONTROLS_SCREENSHOTS/controls-and-pending.json"
# Test return joins App and closes Chrome before this separate 180-second window.
printf 'M6_COMPACT_CONTROLS_SCREENSHOTS_READY %s\n' "$PULSE_M6_COMPACT_CONTROLS_SCREENSHOTS"
inspected=0
for ((attempt=0;attempt<180;attempt++)); do
 if [[ -f "$PULSE_M6_COMPACT_CONTROLS_SCREENSHOTS/inspected" ]]; then inspected=1; break; fi
 sleep 1
done
if [[ $inspected != 1 ]]; then echo M6_COMPACT_CONTROLS_SCREENSHOT_INSPECTION_TIMEOUT >&2; exit 1; fi
echo 'M6_COMPACT_CONTROLS_ACCEPTANCE_EXECUTED tests=1 skipped=0 commands=4 explicit_retries=3 unrelated_business_writes=0 SQL_display_only=false'
