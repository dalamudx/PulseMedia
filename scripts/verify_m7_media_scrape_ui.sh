#!/usr/bin/env bash
# SQL-prepared UI inventory; genuine ingestion/NFO/JPEG are separate acceptance.
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-all}
if [[ $# -gt 1 || ( $mode != all && $mode != check ) ]]; then echo 'Usage: verify_m7_media_scrape_ui.sh [all|check]' >&2; exit 2; fi
if [[ ${PULSE_M7_MEDIA_SCRAPE_DEADLINE:-0} != 1 ]]; then
 exec timeout --signal=TERM --kill-after=20s 900s env PULSE_M7_MEDIA_SCRAPE_DEADLINE=1 mise exec -- bash scripts/verify_m7_media_scrape_ui.sh "$mode"
fi
if [[ $mode == all && ${PULSE_M7_MEDIA_SCRAPE_OWNED_PG:-0} != 1 ]]; then
 exec env PULSE_M7_MEDIA_SCRAPE_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m7_media_scrape_ui.sh "$mode"
fi
owned=$(mktemp -d /data/temp/pulse-m7-media-scrape.XXXXXX)
cleanup(){
 rc=$?
 trap - EXIT
 case "$owned" in /data/temp/pulse-m7-media-scrape.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M7_MEDIA_SCRAPE_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache"
export XDG_CACHE_HOME="$owned/cache" XDG_CONFIG_HOME="$owned/config"
export PULSE_M7_MEDIA_SCRAPE_REPO="$repo" PULSE_M7_MEDIA_SCRAPE_ASSETS="$owned/web" PULSE_M7_MEDIA_SCRAPE_SCREENSHOTS="$owned/screenshots"
mkdir -p "$GOTMPDIR" "$PULSE_M7_MEDIA_SCRAPE_SCREENSHOTS"
files=(
 ./internal/httpapi/m5_indexer_acceptance_test.go
 ./internal/httpapi/m5_interactive_search_acceptance_test.go
 ./internal/httpapi/m5_subscription_creation_acceptance_test.go
 ./internal/httpapi/file_mapping_browser_helper_test.go
 ./internal/httpapi/m7_media_scrape_browser_test.go
)
bash -n scripts/verify_m7_media_scrape_ui.sh
node --check web/tests/m7-media-scrape-browser.mjs
cat > "$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const env=process.env,load=createRequire(env.PULSE_M7_MEDIA_SCRAPE_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
let bridges=0;
const observer={name:'scrape-private-auth-observer',enforce:'pre',
 transform(source,id){
  if(!id.endsWith('/features/auth/AuthContext.tsx'))return null;
  const anchor='return <AuthContext.Provider';
  if(source.split(anchor).length!==2)throw Error('AuthContext observer anchor changed');
  bridges++;
  return source.replace(anchor,'window.__scrapeAuth = {refreshIdentity: async () => {const r = await api.GET("/api/v1/auth/status", {cache:"no-store"}); if (!r.data?.current_user) throw Error("actual identity missing"); setUser(r.data.current_user);}};\n  '+anchor);
 },
 closeBundle(){if(bridges!==1)throw Error('Expected exactly one private auth observer');}
};
export default {root:env.PULSE_M7_MEDIA_SCRAPE_REPO+'/web',envDir:env.TMPDIR,plugins:[observer,react()],cacheDir:env.TMPDIR+'/vite-cache',resolve:{alias:{'@':env.PULSE_M7_MEDIA_SCRAPE_REPO+'/web/src'}},build:{outDir:env.PULSE_M7_MEDIA_SCRAPE_ASSETS,emptyOutDir:true}};
CONFIG
node --check "$owned/vite.config.mjs"
if [[ $mode == check ]]; then
 mise exec -- go test -race -c -o "$owned/scrape.test" "${files[@]}"
 test -s "$owned/scrape.test"
 echo M7_MEDIA_SCRAPE_RUNTIME_READY
 exit 0
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
export PULSE_M7_MEDIA_SCRAPE_BROWSER=1
mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/web-build.log" 2>&1 || { tail -n 30 "$owned/web-build.log"; exit 1; }
mise exec -- go test -race "${files[@]}" -run '^TestM7MediaScrapeActualChromeHTTPPG$' -count=1 -v -timeout=240s 2>&1 | tee "$owned/acceptance.log"
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M7_MEDIA_SCRAPE_UNEXPECTED_SKIP >&2; exit 1; fi
test "$(grep -c '^--- PASS: TestM7MediaScrapeActualChromeHTTPPG (' "$owned/acceptance.log")" = 1
test "$(grep -c '^--- PASS:' "$owned/acceptance.log")" = 1
for marker in M7_MEDIA_SCRAPE_BROWSER_PASS M7_MEDIA_SCRAPE_APP_COOKIE_PG_PASS M7_MEDIA_SCRAPE_CHROME_CLEANUP_VERIFIED; do
 test "$(grep -c "$marker" "$owned/acceptance.log")" = 1
done
for width in 1280 375 320; do
 for state in filled pending receipt; do test -s "$PULSE_M7_MEDIA_SCRAPE_SCREENSHOTS/$state-$width.png"; done
done
# App/Go/Chrome have exited; this independent review window owns the screenshots.
printf 'M7_MEDIA_SCRAPE_SCREENSHOTS_READY %s\n' "$PULSE_M7_MEDIA_SCRAPE_SCREENSHOTS"
inspected=0
for ((attempt=0;attempt<300;attempt++)); do
 if [[ -f "$PULSE_M7_MEDIA_SCRAPE_SCREENSHOTS/inspected" ]]; then inspected=1; break; fi
 sleep 1
done
if [[ $inspected != 1 ]]; then echo M7_MEDIA_SCRAPE_SCREENSHOT_INSPECTION_TIMEOUT >&2; exit 1; fi
echo 'M7_MEDIA_SCRAPE_ACCEPTANCE_EXECUTED tests=1 skipped=0 api_POST=1 SQL_prepared=true genuine_ingestion=false filesystem_success=false'
