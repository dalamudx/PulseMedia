#!/usr/bin/env bash
# mise exec -- bash scripts/verify_m5_discovery.sh [all|catalog|tv|movie|boards|details]
# Run after Main's RUNTIME_READY. Inspect SCREENSHOTS_READY images, then create
# screenshots/inspected within 180 seconds; all task artifacts are deleted.
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [ "$#" -gt 1 ]; then echo 'Usage: verify_m5_discovery.sh [all|catalog|tv|movie|boards|details]' >&2; exit 2; fi
mode=${1:-all}
case "$mode" in
 all)
  pattern='^TestM5(Discovery(CatalogSubscription|TVHandoff|MoviePanel)ActualChrome|InteractiveSearchSelectedMovieAcquisitionNative)HTTPPG$'
  expected=4
  receipts='M5_DISCOVERY_CATALOG_PASS M5_DISCOVERY_TV_PASS M5_DISCOVERY_MOVIE_PASS';;
 catalog) pattern='^TestM5DiscoveryCatalogSubscriptionActualChromeHTTPPG$'; expected=1; receipts=M5_DISCOVERY_CATALOG_PASS;;
 tv) pattern='^TestM5DiscoveryTVHandoffActualChromeHTTPPG$'; expected=1; receipts=M5_DISCOVERY_TV_PASS;;
 movie) pattern='^TestM5DiscoveryMoviePanelActualChromeHTTPPG$'; expected=1; receipts=M5_DISCOVERY_MOVIE_PASS;;
 boards) pattern='^TestM6DiscoveryBoardsActualChromeHTTPPG$'; expected=1; receipts=M6_DISCOVERY_BOARDS_PASS;;
 details) pattern='^TestM6MediaDetailActualChromeHTTPPG$'; expected=1; receipts=M6_MEDIA_DETAIL_PASS;;

 douban) pattern='^TestM7DiscoveryDoubanActualChromeHTTPPG$'; expected=1; receipts=M7_DISCOVERY_DOUBAN_PASS;;
 *) echo 'Usage: verify_m5_discovery.sh [all|catalog|tv|movie|boards|details]' >&2; exit 2;;
esac
if [ "${PULSE_M5_DISCOVERY_OWNED_PG:-}" != 1 ]; then
 exec env PULSE_M5_DISCOVERY_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m5_discovery.sh "$mode"
fi
: "${PULSE_TEST_DATABASE_URL:?private PostgreSQL fixture required}"
owned=$(mktemp -d /data/temp/pulse-m5-discovery.XXXXXX)
cleanup(){
 rc=$?
 trap - EXIT
 if [ -f "$owned/screenshots/discovery-desktop.png" ] || [ -f "$owned/screenshots/discovery-375.png" ] || [ -f "$owned/screenshots/fetch-preview-1280.png" ]; then
  echo "M5_DISCOVERY_SCREENSHOTS_READY $owned/screenshots"
  inspected=0
  review_seconds=180
  for ((attempt=0;attempt<review_seconds;attempt++)); do
   if [ -f "$owned/screenshots/inspected" ]; then inspected=1; break; fi
   sleep 1
  done
  if [ "$inspected" != 1 ]; then echo M5_DISCOVERY_SCREENSHOT_INSPECTION_TIMEOUT >&2; rc=1; fi
 fi
 case "$owned" in /data/temp/pulse-m5-discovery.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M5_DISCOVERY_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build"
export PULSE_M5_CREATION_REPO="$repo" PULSE_M5_CREATION_ASSETS="$owned/built-web"
export PULSE_M5_DISCOVERY_SCREENSHOTS="$owned/screenshots"
export PULSE_M5_INDEXER_ACCEPTANCE=1 PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE=1 PULSE_M5_DISCOVERY_BROWSER=1
mkdir -p "$GOTMPDIR" "$PULSE_M5_DISCOVERY_SCREENSHOTS"
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
 ./internal/httpapi/m5_interactive_search_acquisition_test.go
 ./internal/httpapi/m5_subscription_creation_acceptance_test.go
 ./internal/httpapi/m5_subscription_creation_browser_test.go
 ./internal/httpapi/m5_automatic_tv_acceptance_test.go
 ./internal/httpapi/m5_discovery_browser_test.go
 ./internal/httpapi/m6_media_detail_browser_test.go
)
if ! mise exec -- go test -race "${files[@]}" -run "$pattern" -count=1 -v -timeout=7m >"$owned/acceptance.log" 2>&1; then
 tail -n 100 "$owned/acceptance.log"; exit 1
fi
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M5_DISCOVERY_UNEXPECTED_SKIP >&2; exit 1; fi
actual=$(grep -Ec '^--- PASS: TestM[567]' "$owned/acceptance.log")
if [ "$actual" != "$expected" ]; then echo "M5_DISCOVERY_MISSING_EXECUTION expected=$expected actual=$actual" >&2; exit 1; fi
for receipt in $receipts; do grep -q "$receipt" "$owned/acceptance.log" || exit 1; done
if [ "$mode" = all ] || [ "$mode" = catalog ] || [ "$mode" = boards ] || [ "$mode" = details ] || [ "$mode" = douban ]; then
 for screenshot in discovery-desktop discovery-375; do test -s "$owned/screenshots/$screenshot.png" || exit 1; done
 if [ "$mode" = boards ] || [ "$mode" = details ]; then test -s "$owned/screenshots/discovery-320.png" || exit 1; fi
fi
grep -E 'M[567]_(DISCOVERY_|MEDIA_DETAIL_)|--- PASS:|^ok[[:space:]]' "$owned/acceptance.log"
echo "M5_DISCOVERY_ACCEPTANCE_EXECUTED mode=$mode tests=$actual"
