#!/usr/bin/env bash
# Run: mise exec -- bash scripts/verify_m5_interactive_search.sh [http|browser|all]
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-all}
case "$mode" in http|browser|all) ;; *) echo 'Usage: verify_m5_interactive_search.sh [http|browser|all]' >&2; exit 2;; esac
# Test functions check their own opt-in before creating any database. This runner
# explicitly opts into a new pinned private PostgreSQL cluster, never live services.
if [ "${PULSE_M5_INTERACTIVE_OWNED_PG:-}" != 1 ]; then
  exec env PULSE_M5_INTERACTIVE_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m5_interactive_search.sh "$mode"
fi
: "${PULSE_TEST_DATABASE_URL:?private PostgreSQL fixture required}"
owned=$(mktemp -d /data/temp/pulse-m5-interactive.XXXXXX)
cleanup(){
  rc=$?
  trap - EXIT
  case "$owned" in /data/temp/pulse-m5-interactive.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
  test ! -e "$owned" || exit 1
  echo M5_INTERACTIVE_OWNED_TEMP_CLEANUP_VERIFIED
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
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
  mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/web-build.log" 2>&1 || { tail -n 25 "$owned/web-build.log"; exit 1; }
  export PULSE_M5_INTERACTIVE_BROWSER=1
fi
pattern='^TestM5InteractiveSearch.*HTTPPG$'
if [ "$mode" = http ]; then pattern='^TestM5InteractiveSearch(ProgressiveNative|StrictValidationAndConcurrentReceipt|CancellationFence|CurrentOwnerSessionScope|NativePaginationBound|PersistentBudgetsAndSourceCap|NativePageTimeoutDoesNotBlockFast|SelectedMovieAcquisitionNative)HTTPPG$'; fi
if [ "$mode" = browser ]; then pattern='^TestM5InteractiveSearchActualChromeHTTPPG$'; fi
if ! mise exec -- go test -race ./internal/httpapi/m5_indexer_acceptance_test.go ./internal/httpapi/m5_interactive_search_acceptance_test.go ./internal/httpapi/m5_interactive_search_browser_test.go ./internal/httpapi/m5_interactive_search_acquisition_test.go -run "$pattern" -count=1 -v -timeout=8m >"$owned/acceptance.log" 2>&1; then
  tail -n 55 "$owned/acceptance.log"; exit 1
fi
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M5_INTERACTIVE_UNEXPECTED_SKIP >&2; exit 1; fi
grep -E 'M5_INTERACTIVE_|--- PASS:|^ok[[:space:]]' "$owned/acceptance.log"
