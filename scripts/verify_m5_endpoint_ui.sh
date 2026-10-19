#!/usr/bin/env bash
# PULSE_M5_ENDPOINT_BROWSER=1 mise exec -- bash scripts/verify_m5_endpoint_ui.sh [all|states|recovery]
# Optional live screenshot review: PULSE_M5_ENDPOINT_SCREENSHOT_REVIEW=1.
# Read each SCREENSHOTS_READY pair, then create <prefix>-inspected within 90s.
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [ "$#" -gt 1 ]; then echo 'Usage: verify_m5_endpoint_ui.sh [all|states|recovery]' >&2; exit 2; fi
mode=${1:-all}
case "$mode" in
 all) pattern='^TestM5EndpointConfiguration(States|Recovery)ActualChromeHTTPPG$'; expected=2; receipts='M5_ENDPOINT_STATES_PASS M5_ENDPOINT_RECOVERY_PASS';;
 states) pattern='^TestM5EndpointConfigurationStatesActualChromeHTTPPG$'; expected=1; receipts=M5_ENDPOINT_STATES_PASS;;
 recovery) pattern='^TestM5EndpointConfigurationRecoveryActualChromeHTTPPG$'; expected=1; receipts=M5_ENDPOINT_RECOVERY_PASS;;
 *) echo 'Usage: verify_m5_endpoint_ui.sh [all|states|recovery]' >&2; exit 2;;
esac
# Refuse before touching PostgreSQL, building assets or opening Chrome.
if [ "${PULSE_M5_ENDPOINT_BROWSER:-}" != 1 ]; then echo 'Set PULSE_M5_ENDPOINT_BROWSER=1 for owned actual Chrome/PG acceptance.' >&2; exit 2; fi
if [ "${PULSE_M5_ENDPOINT_OWNED_PG:-}" != 1 ]; then
 exec env PULSE_M5_ENDPOINT_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m5_endpoint_ui.sh "$mode"
fi
: "${PULSE_TEST_DATABASE_URL:?private PostgreSQL fixture required}"
owned=$(mktemp -d /data/temp/pulse-m5-endpoint-ui.XXXXXX)
cleanup(){
 rc=$?
 trap - EXIT
 case "$owned" in /data/temp/pulse-m5-endpoint-ui.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M5_ENDPOINT_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache"
export XDG_CACHE_HOME="$owned/cache" XDG_CONFIG_HOME="$owned/config"
export PULSE_M5_ENDPOINT_REPO="$repo" PULSE_M5_ENDPOINT_ASSETS="$owned/built-web" PULSE_M5_ENDPOINT_SCREENSHOTS="$owned/screenshots"
mkdir -p "$GOTMPDIR" "$GOCACHE" "$PULSE_M5_ENDPOINT_SCREENSHOTS"
cat > "$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const load=createRequire(process.env.PULSE_M5_ENDPOINT_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
export default {
 root:process.env.PULSE_M5_ENDPOINT_REPO+'/web',envDir:process.env.TMPDIR,plugins:[react()],
 cacheDir:process.env.TMPDIR+'/vite-cache',
 resolve:{alias:{'@':process.env.PULSE_M5_ENDPOINT_REPO+'/web/src'}},
 build:{outDir:process.env.PULSE_M5_ENDPOINT_ASSETS,emptyOutDir:true}
};
CONFIG
mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/web-build.log" 2>&1 || { tail -n 30 "$owned/web-build.log"; exit 1; }
if ! mise exec -- go test -race ./internal/app -run "$pattern" -count=1 -v -timeout=240s 2>&1 | tee "$owned/acceptance.log"; then exit 1; fi
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M5_ENDPOINT_UNEXPECTED_SKIP >&2; exit 1; fi
actual=$(grep -c '^--- PASS: TestM5EndpointConfiguration' "$owned/acceptance.log")
if [ "$actual" != "$expected" ]; then echo "M5_ENDPOINT_MISSING_EXECUTION expected=$expected actual=$actual" >&2; exit 1; fi
for receipt in $receipts; do grep -q "$receipt" "$owned/acceptance.log" || exit 1; done
for selected in states recovery; do
 if [ "$mode" = all ] || [ "$mode" = "$selected" ]; then
  for family in download_client media_server; do
   for viewport in desktop 375; do test -s "$owned/screenshots/$selected-$family-$viewport.png" || exit 1; done
   if [ "${PULSE_M5_ENDPOINT_SCREENSHOT_REVIEW:-}" = 1 ]; then test -f "$owned/screenshots/$selected-$family-inspected" || exit 1; fi
  done
 fi
done
echo "M5_ENDPOINT_UI_EXECUTED mode=$mode tests=$actual skipped=0"