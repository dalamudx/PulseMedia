#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ ${PULSE_M10_BANGUMI_OWNED_PG:-} != 1 ]];then exec env PULSE_M10_BANGUMI_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m10_bangumi_browser.sh;fi
: "${PULSE_TEST_DATABASE_URL:?owned PG required}"
owned=$(mktemp -d /data/temp/pulse-m10-bangumi-browser.XXXXXX)
cleanup(){ rc=$?;trap - EXIT;test ! -L "$owned";test "$(realpath -- "$owned")" = "$owned";rm -rf -- "$owned";test ! -e "$owned" || exit 1;echo M10_BANGUMI_BROWSER_TEMP_CLEANUP_VERIFIED;exit "$rc"; }
trap cleanup EXIT
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" XDG_CACHE_HOME="$owned/cache" XDG_CONFIG_HOME="$owned/config"
export PULSE_M10_BANGUMI_BROWSER=1 PULSE_M10_BANGUMI_REPO="$repo" PULSE_M10_BANGUMI_ASSETS="$owned/web" PULSE_M10_BANGUMI_SHOTS="$owned/shots" PULSE_M10_BANGUMI_REVIEW="${PULSE_M10_BANGUMI_REVIEW:-1}"
mkdir -p "$GOTMPDIR" "$PULSE_M10_BANGUMI_SHOTS"
cat >"$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const load=createRequire(process.env.PULSE_M10_BANGUMI_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
export default {root:process.env.PULSE_M10_BANGUMI_REPO+'/web',envDir:process.env.TMPDIR,plugins:[react()],cacheDir:process.env.TMPDIR+'/vite-cache',resolve:{alias:{'@':process.env.PULSE_M10_BANGUMI_REPO+'/web/src'}},build:{outDir:process.env.PULSE_M10_BANGUMI_ASSETS,emptyOutDir:true}};
CONFIG
mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/build.log" 2>&1 || { tail -n 45 "$owned/build.log";exit 1; }
mise exec -- go test -race ./internal/app -run '^TestM10BangumiChromePG$' -count=1 -v -timeout=10m 2>&1 | tee "$owned/chrome.log"
if grep -q -- '--- SKIP:' "$owned/chrome.log";then exit 1;fi
test "$(grep -c '^--- PASS: TestM10BangumiChromePG (' "$owned/chrome.log")" = 1
test "$(grep -c '^M10_BANGUMI_CHROME_PASS checks=' "$owned/chrome.log")" = 1
for name in bangumi-1440 bangumi-768 bangumi-375 bangumi-320 bangumi-review bangumi-unavailable;do test -s "$PULSE_M10_BANGUMI_SHOTS/$name.png";done
if [[ "$PULSE_M10_BANGUMI_REVIEW" == 1 ]];then test -f "$PULSE_M10_BANGUMI_SHOTS/inspected";fi
echo M10_BANGUMI_BROWSER_FORMAL_PASS_parents=1_skipped=0
