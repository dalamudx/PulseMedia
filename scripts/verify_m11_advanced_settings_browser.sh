#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ ${PULSE_M11_ADVANCED_BROWSER_PG:-} != 1 ]];then exec env PULSE_M11_ADVANCED_BROWSER_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m11_advanced_settings_browser.sh;fi
: "${PULSE_TEST_DATABASE_URL:?owned PG required}"
owned=$(mktemp -d /data/temp/pulse-m11-advanced-browser.XXXXXX)
cleanup(){ rc=$?;trap - EXIT;test ! -L "$owned";test "$(realpath -- "$owned")" = "$owned";rm -rf -- "$owned";test ! -e "$owned" || exit 1;echo M11_ADVANCED_BROWSER_TEMP_CLEANUP_VERIFIED;exit "$rc"; }
trap cleanup EXIT
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" XDG_CACHE_HOME="$owned/cache" XDG_CONFIG_HOME="$owned/config"
export PULSE_M11_ADVANCED_BROWSER=1 PULSE_M11_ADVANCED_REPO="$repo" PULSE_M11_ADVANCED_ASSETS="$owned/web" PULSE_M11_ADVANCED_SHOTS="$owned/shots" PULSE_M11_ADVANCED_REVIEW="${PULSE_M11_ADVANCED_REVIEW:-1}"
mkdir -p "$GOTMPDIR" "$PULSE_M11_ADVANCED_SHOTS"
cat >"$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const load=createRequire(process.env.PULSE_M11_ADVANCED_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
export default {root:process.env.PULSE_M11_ADVANCED_REPO+'/web',envDir:process.env.TMPDIR,plugins:[react()],cacheDir:process.env.TMPDIR+'/vite-cache',resolve:{alias:{'@':process.env.PULSE_M11_ADVANCED_REPO+'/web/src'}},build:{outDir:process.env.PULSE_M11_ADVANCED_ASSETS,emptyOutDir:true}};
CONFIG
mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/build.log" 2>&1 || { tail -n 45 "$owned/build.log";exit 1; }
mise exec -- go test -race ./internal/app -run '^TestM11AdvancedSettingsChromePG$' -count=1 -v -timeout=16m 2>&1 | tee "$owned/chrome.log"
if grep -q -- '--- SKIP:' "$owned/chrome.log";then exit 1;fi
test "$(grep -c '^--- PASS: TestM11AdvancedSettingsChromePG (' "$owned/chrome.log")" = 1
test "$(grep -c '^M11_ADVANCED_CHROME_PASS checks=' "$owned/chrome.log")" = 1
for name in advanced-1440 advanced-768 advanced-375 advanced-320 advanced-unavailable advanced-audit;do test -s "$PULSE_M11_ADVANCED_SHOTS/$name.png";done
if [[ "$PULSE_M11_ADVANCED_REVIEW" == 1 ]];then test -f "$PULSE_M11_ADVANCED_SHOTS/inspected";fi
echo M11_ADVANCED_BROWSER_FORMAL_PASS_parents=1_skipped=0
