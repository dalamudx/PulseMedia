#!/usr/bin/env bash
# Development validation: mise exec -- bash scripts/verify_m6_subscription_calendar_ui.sh [all]
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# -gt 1 || ${1:-all} != all ]]; then echo 'Usage: verify_m6_subscription_calendar_ui.sh [all]' >&2; exit 2; fi
export PULSE_M6_CALENDAR_BROWSER=1
if [[ ${PULSE_M6_CALENDAR_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M6_CALENDAR_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m6_subscription_calendar_ui.sh all
fi
: "${PULSE_TEST_DATABASE_URL:?private PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m6-calendar.XXXXXX)
cleanup(){
 rc=$?
 trap - EXIT
 case "$owned" in /data/temp/pulse-m6-calendar.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M6_SUBSCRIPTION_CALENDAR_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache"
export XDG_CACHE_HOME="$owned/cache" XDG_CONFIG_HOME="$owned/config"
export PULSE_M6_CALENDAR_REPO="$repo" PULSE_M6_CALENDAR_ASSETS="$owned/web" PULSE_M6_CALENDAR_SCREENSHOTS="$owned/screenshots"
mkdir -p "$GOTMPDIR" "$PULSE_M6_CALENDAR_SCREENSHOTS"
cat > "$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const load=createRequire(process.env.PULSE_M6_CALENDAR_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
let bridges=0;
const authBridge={name:'m6-calendar-private-auth-observer',enforce:'pre',
 transform(source,id){
  if(!id.endsWith('/features/auth/AuthContext.tsx'))return null;
  const anchor='return <AuthContext.Provider';
  if(source.split(anchor).length!==2)throw new Error('AuthContext bridge anchor changed');
  bridges++;
  return source.replace(anchor,'window.__m6CalendarAuth = {checkStatus};\n  '+anchor);
 },
 closeBundle(){if(bridges!==1)throw new Error('Expected exactly one private AuthContext bridge');}
};
export default {
 root:process.env.PULSE_M6_CALENDAR_REPO+'/web',envDir:process.env.TMPDIR,plugins:[authBridge,react()],cacheDir:process.env.TMPDIR+'/vite-cache',
 resolve:{alias:{'@':process.env.PULSE_M6_CALENDAR_REPO+'/web/src'}},
 build:{outDir:process.env.PULSE_M6_CALENDAR_ASSETS,emptyOutDir:true}
};
CONFIG
mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/web-build.log" 2>&1 || { tail -n 30 "$owned/web-build.log"; exit 1; }
# The explicit file set reuses only creation/HTTP/SQL and owned Chrome helpers.
# It deliberately does not invoke m5DiscoveryBrowser or its discovery workers.
mise exec -- go test -race \
 ./internal/httpapi/m5_indexer_acceptance_test.go \
 ./internal/httpapi/m5_interactive_search_acceptance_test.go \
 ./internal/httpapi/m5_subscription_creation_acceptance_test.go \
 ./internal/httpapi/file_mapping_browser_helper_test.go \
 ./internal/httpapi/m6_subscription_calendar_browser_test.go \
 -run '^TestM6SubscriptionCalendarActualChromeHTTPPG$' -count=1 -v -timeout=180s 2>&1 | tee "$owned/acceptance.log"
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M6_CALENDAR_UNEXPECTED_SKIP >&2; exit 1; fi
test "$(grep -c '^--- PASS: TestM6SubscriptionCalendarActualChromeHTTPPG (' "$owned/acceptance.log")" = 1
for marker in M6_SUBSCRIPTION_CALENDAR_BROWSER_PASS M6_SUBSCRIPTION_CALENDAR_APP_COOKIE_PG_PASS M6_SUBSCRIPTION_CALENDAR_CHROME_CLEANUP_VERIFIED M6_SUBSCRIPTION_CALENDAR_SCREENSHOTS_READY; do
 test "$(grep -c "$marker" "$owned/acceptance.log")" = 1
done
for view in cards month week; do
 for width in 1280 768 375 320; do test -s "$PULSE_M6_CALENDAR_SCREENSHOTS/$view-$width.png"; done
done
# Inspection is outside the finite Go/browser budget. Default validation never
# waits; Main reads PNGs before creating this task-owned inspected marker.
if [[ ${PULSE_M6_CALENDAR_REVIEW:-0} == 1 ]]; then
 inspected=0
 for ((attempt=0;attempt<180;attempt++)); do
  if [[ -f "$PULSE_M6_CALENDAR_SCREENSHOTS/inspected" ]]; then inspected=1; break; fi
  sleep 1
 done
 if [[ $inspected != 1 ]]; then echo M6_CALENDAR_SCREENSHOT_INSPECTION_TIMEOUT >&2; exit 1; fi
fi
echo 'M6_SUBSCRIPTION_CALENDAR_ACCEPTANCE_EXECUTED tests=1 skipped=0 business_mutations=0 progress=real_initial_0/3'
