#!/usr/bin/env bash
# Main-owned runtime: mise exec -- bash scripts/verify_m6_journey_ui.sh
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m6_journey_ui.sh' >&2; exit 2; fi
export PULSE_M6_JOURNEY_BROWSER=1
if [[ ${PULSE_M6_JOURNEY_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M6_JOURNEY_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m6_journey_ui.sh
fi
: "${PULSE_TEST_DATABASE_URL:?private PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m6-journey.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m6-journey.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M6_JOURNEY_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
module_cache=$(mise exec -- go env GOMODCACHE)
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache" GOMODCACHE="$owned/modules"
export GOPROXY="file://$module_cache/cache/download"
export XDG_CACHE_HOME="$owned/cache" XDG_CONFIG_HOME="$owned/config"
export PULSE_M6_JOURNEY_REPO="$repo" PULSE_M6_JOURNEY_ASSETS="$owned/web" PULSE_M6_JOURNEY_SCREENSHOTS="$owned/screenshots"
mkdir -p "$GOTMPDIR" "$GOCACHE" "$GOMODCACHE" "$PULSE_M6_JOURNEY_SCREENSHOTS"
cat > "$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const load=createRequire(process.env.PULSE_M6_JOURNEY_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
export default {
 root:process.env.PULSE_M6_JOURNEY_REPO+'/web',envDir:process.env.TMPDIR,plugins:[react()],cacheDir:process.env.TMPDIR+'/vite-cache',
 resolve:{alias:{'@':process.env.PULSE_M6_JOURNEY_REPO+'/web/src'}},
 build:{outDir:process.env.PULSE_M6_JOURNEY_ASSETS,emptyOutDir:true}
};
CONFIG
mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/web-build.log" 2>&1 || { tail -n 30 "$owned/web-build.log"; exit 1; }
# Go returns only after App and owned Chrome cleanup. Review has its own budget.
mise exec -- go test -race ./internal/app -run '^TestM6JourneyActualChromeHTTPPG$' -count=1 -v -timeout=360s 2>&1 | tee "$owned/acceptance.log"
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M6_JOURNEY_UNEXPECTED_SKIP >&2; exit 1; fi
test "$(grep -c '^--- PASS: TestM6JourneyActualChromeHTTPPG (' "$owned/acceptance.log")" = 1
for marker in M6_JOURNEY_BROWSER_PASS M6_JOURNEY_APP_COOKIE_PG_REALFS_PASS; do test "$(grep -c "$marker" "$owned/acceptance.log")" = 1; done
for name in library-desktop library-375 subscription-desktop subscription-375 activity-desktop activity-375; do test -s "$PULSE_M6_JOURNEY_SCREENSHOTS/$name.png"; done
echo "M6_JOURNEY_SCREENSHOTS_READY $PULSE_M6_JOURNEY_SCREENSHOTS"
if [[ ${PULSE_M6_JOURNEY_REVIEW:-} == 1 ]]; then
 inspected=0
 for ((attempt=0;attempt<180;attempt++)); do
  if [[ -f "$PULSE_M6_JOURNEY_SCREENSHOTS/inspected" ]]; then inspected=1; break; fi
  sleep 1
 done
 if [[ $inspected != 1 ]]; then echo M6_JOURNEY_SCREENSHOT_REVIEW_TIMEOUT >&2; exit 1; fi
fi
echo M6_JOURNEY_ACCEPTANCE_EXECUTED_tests=1_skipped=0_source=production_automatic_tv_finalizer
