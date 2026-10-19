#!/usr/bin/env bash
# Development acceptance. SQL catalog rows prove display only; independent genuine
# ingestion remains covered by TestLibraryCatalogNativeAndCookie and pipeline suites.
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-all}
if [[ $# -gt 1 || ( $mode != all && $mode != check ) ]]; then echo 'Usage: verify_m6_media_library_ui.sh [all|check]' >&2; exit 2; fi
if [[ ${PULSE_M6_MEDIA_LIBRARY_DEADLINE:-0} != 1 ]]; then
 exec timeout --signal=TERM --kill-after=20s 600s env PULSE_M6_MEDIA_LIBRARY_DEADLINE=1 mise exec -- bash scripts/verify_m6_media_library_ui.sh "$mode"
fi
if [[ $mode == all && ${PULSE_M6_MEDIA_LIBRARY_OWNED_PG:-0} != 1 ]]; then
 exec env PULSE_M6_MEDIA_LIBRARY_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m6_media_library_ui.sh "$mode"
fi
owned=$(mktemp -d /data/temp/pulse-m6-media-library.XXXXXX)
cleanup(){
 rc=$?
 trap - EXIT
 case "$owned" in /data/temp/pulse-m6-media-library.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M6_MEDIA_LIBRARY_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache"
export XDG_CACHE_HOME="$owned/cache" XDG_CONFIG_HOME="$owned/config"
export PULSE_M6_MEDIA_LIBRARY_REPO="$repo" PULSE_M6_MEDIA_LIBRARY_ASSETS="$owned/web" PULSE_M6_MEDIA_LIBRARY_SCREENSHOTS="$owned/screenshots"
mkdir -p "$GOTMPDIR" "$PULSE_M6_MEDIA_LIBRARY_SCREENSHOTS"
files=(
 ./internal/httpapi/m5_indexer_acceptance_test.go
 ./internal/httpapi/m5_interactive_search_acceptance_test.go
 ./internal/httpapi/m5_subscription_creation_acceptance_test.go
 ./internal/httpapi/file_mapping_browser_helper_test.go
 ./internal/httpapi/m6_media_library_browser_test.go
)
bash -n scripts/verify_m6_media_library_ui.sh
node --check web/tests/media-library-browser.mjs
cat > "$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const env=process.env,load=createRequire(env.PULSE_M6_MEDIA_LIBRARY_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
let bridges=0;
const observer={name:'library-private-auth-observer',enforce:'pre',
 transform(source,id){
  if(!id.endsWith('/features/auth/AuthContext.tsx'))return null;
  const anchor='return <AuthContext.Provider';
  if(source.split(anchor).length!==2)throw Error('AuthContext observer anchor changed');
  bridges++;
  // Real status reply updates identity without the stronger checkStatus unmount.
  // This exercises MediaView's own keyed account/role remount and abort fence.
  return source.replace(anchor,'window.__libraryAuth = {refreshIdentity: async () => {const r = await api.GET("/api/v1/auth/status", {cache:"no-store"}); if (!r.data?.current_user) throw Error("actual identity missing"); setUser(r.data.current_user);}};\n  '+anchor);
 },
 closeBundle(){if(bridges!==1)throw Error('Expected exactly one private auth observer');}
};
export default {root:env.PULSE_M6_MEDIA_LIBRARY_REPO+'/web',envDir:env.TMPDIR,plugins:[observer,react()],cacheDir:env.TMPDIR+'/vite-cache',resolve:{alias:{'@':env.PULSE_M6_MEDIA_LIBRARY_REPO+'/web/src'}},build:{outDir:env.PULSE_M6_MEDIA_LIBRARY_ASSETS,emptyOutDir:true}};
CONFIG
node --check "$owned/vite.config.mjs"
if [[ $mode == check ]]; then
 mise exec -- go test -race -c -o "$owned/library.test" "${files[@]}"
 test -s "$owned/library.test"
 echo M6_MEDIA_LIBRARY_RUNTIME_READY
 exit 0
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
export PULSE_M6_MEDIA_LIBRARY_BROWSER=1
mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/web-build.log" 2>&1 || { tail -n 30 "$owned/web-build.log"; exit 1; }
mise exec -- go test -race "${files[@]}" -run '^TestM6MediaLibraryActualChromeHTTPPG$' -count=1 -v -timeout=240s 2>&1 | tee "$owned/acceptance.log"
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M6_MEDIA_LIBRARY_UNEXPECTED_SKIP >&2; exit 1; fi
test "$(grep -c '^--- PASS: TestM6MediaLibraryActualChromeHTTPPG (' "$owned/acceptance.log")" = 1
test "$(grep -c '^--- PASS:' "$owned/acceptance.log")" = 1
for marker in M6_MEDIA_LIBRARY_BROWSER_PASS M6_MEDIA_LIBRARY_APP_COOKIE_PG_PASS M6_MEDIA_LIBRARY_CHROME_CLEANUP_VERIFIED; do
 test "$(grep -c "$marker" "$owned/acceptance.log")" = 1
done
for width in 1280 768 375 320; do
 for view in movie tv files; do test -s "$PULSE_M6_MEDIA_LIBRARY_SCREENSHOTS/$view-$width.png"; done
done
# Go/App/Chrome have terminated before the independent review window starts.
printf 'M6_MEDIA_LIBRARY_SCREENSHOTS_READY %s\n' "$PULSE_M6_MEDIA_LIBRARY_SCREENSHOTS"
inspected=0
for ((attempt=0;attempt<180;attempt++)); do
 if [[ -f "$PULSE_M6_MEDIA_LIBRARY_SCREENSHOTS/inspected" ]]; then inspected=1; break; fi
 sleep 1
done
if [[ $inspected != 1 ]]; then echo M6_MEDIA_LIBRARY_SCREENSHOT_INSPECTION_TIMEOUT >&2; exit 1; fi
echo 'M6_MEDIA_LIBRARY_ACCEPTANCE_EXECUTED tests=1 skipped=0 business_writes=0 SQL_display_only=true genuine_ingestion=false'