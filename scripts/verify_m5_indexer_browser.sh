#!/usr/bin/env bash
# Named runnable entry: mise exec -- task test-sites-ui -- [http|browser|all]
set -euo pipefail
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-all}
case "$mode" in http|browser|all) ;; *) echo 'Usage: test-sites-ui -- [http|browser|all]' >&2; exit 2;; esac
# Always create an owned PG18 cluster through the established pinned runner.
# The recursive marker is internal; callers need no host PostgreSQL installation.
if [ "${PULSE_M5_OWNED_PG:-}" != 1 ]; then
  exec env PULSE_M5_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m5_indexer_browser.sh "$mode"
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL fixture required}"
owned=$(mktemp -d /data/temp/pulse-m5-indexer.XXXXXX)
cleanup(){
  rc=$?
  trap - EXIT
  case "$owned" in /data/temp/pulse-m5-indexer.*) rm -rf -- "$owned";; *) exit 1;; esac
  test ! -e "$owned" && echo M5_OWNED_TEMP_CLEANUP_VERIFIED
  exit "$rc"
}
trap cleanup EXIT
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build"
export PULSE_M5_INDEXER_ACCEPTANCE=1 PULSE_M5_INDEXER_REPO="$repo"
export PULSE_M5_INDEXER_ASSETS="$owned/built-web"
mkdir -p "$GOTMPDIR"
if [ "$mode" != http ]; then
  cat > "$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const load=createRequire(process.env.PULSE_M5_INDEXER_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
export default {
 root:process.env.PULSE_M5_INDEXER_REPO+'/web',envDir:process.env.TMPDIR,plugins:[react()],
 resolve:{alias:{'@':process.env.PULSE_M5_INDEXER_REPO+'/web/src'}},
 build:{outDir:process.env.PULSE_M5_INDEXER_ASSETS,emptyOutDir:true}
};
CONFIG
  mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/web-build.log" 2>&1 || { tail -n 35 "$owned/web-build.log"; exit 1; }
  export PULSE_M5_INDEXER_BROWSER=1
fi
pattern='^TestM5IndexerPublicCookieAcceptanceHTTPPG$'
if [ "$mode" = browser ]; then pattern='^TestM5IndexerActualBrowserHTTPPG$'; fi
if [ "$mode" = all ]; then pattern='^TestM5Indexer(PublicCookieAcceptance|ActualBrowser)HTTPPG$'; fi
if ! mise exec -- go test -race ./internal/httpapi/m5_indexer_acceptance_test.go ./internal/httpapi/m5_indexer_browser_test.go -run "$pattern" -count=1 -v -timeout=8m >"$owned/acceptance.log" 2>&1; then
  tail -n 75 "$owned/acceptance.log"; exit 1
fi
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then tail -n 50 "$owned/acceptance.log"; echo M5_UNEXPECTED_SKIP >&2; exit 1; fi
grep -E 'M5_INDEXER_|--- PASS:|^ok[[:space:]]' "$owned/acceptance.log"
