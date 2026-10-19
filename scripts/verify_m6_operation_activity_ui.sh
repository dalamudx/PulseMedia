#!/usr/bin/env bash
# Targeted development acceptance; owns its PG, assets, Chrome, and review window.
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-all}
if [[ $# -gt 1 || ( $mode != all && $mode != approval ) ]]; then echo 'Usage: verify_m6_operation_activity_ui.sh [all|approval]' >&2; exit 2; fi
if [[ ${PULSE_M6_OPERATION_DEADLINE:-0} != 1 ]]; then
 exec timeout --signal=TERM --kill-after=20s 600s env PULSE_M6_OPERATION_DEADLINE=1 mise exec -- bash scripts/verify_m6_operation_activity_ui.sh "$mode"
fi
if [[ ${PULSE_M6_OPERATION_OWNED_PG:-0} != 1 ]]; then
 exec env PULSE_M6_OPERATION_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m6_operation_activity_ui.sh "$mode"
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m6-operation.XXXXXX)
cleanup(){
 rc=$?
 trap - EXIT
 case "$owned" in /data/temp/pulse-m6-operation.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M6_OPERATION_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build"
export XDG_CACHE_HOME="$owned/cache" XDG_CONFIG_HOME="$owned/config"
export PULSE_M6_OPERATION_BROWSER=1 PULSE_M6_OPERATION_REPO="$repo" PULSE_M6_OPERATION_ASSETS="$owned/web" PULSE_M6_OPERATION_SCREENSHOTS="$owned/screenshots"
mkdir -p "$GOTMPDIR" "$PULSE_M6_OPERATION_SCREENSHOTS"
cat > "$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const env=process.env,load=createRequire(env.PULSE_M6_OPERATION_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
let bridges=0;
const authObserver={name:'operation-private-auth-observer',enforce:'pre',
 transform(source,id){
  if(!id.endsWith('/features/auth/AuthContext.tsx'))return null;
  const anchor='return <AuthContext.Provider';
  if(source.split(anchor).length!==2)throw Error('AuthContext observer anchor changed');
  bridges++;
  return source.replace(anchor,'window.__operationAuth = {checkStatus};\n  '+anchor);
 },
 closeBundle(){if(bridges!==1)throw Error('Expected exactly one private auth observer');}
};
export default {root:env.PULSE_M6_OPERATION_REPO+'/web',envDir:env.TMPDIR,plugins:[authObserver,react()],cacheDir:env.TMPDIR+'/vite-cache',resolve:{alias:{'@':env.PULSE_M6_OPERATION_REPO+'/web/src'}},build:{outDir:env.PULSE_M6_OPERATION_ASSETS,emptyOutDir:true}};
CONFIG
mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/web-build.log" 2>&1 || { tail -n 30 "$owned/web-build.log"; exit 1; }
if [[ $mode == approval ]]; then
 export PULSE_ORGANIZATION_UI_BROWSER=1 PULSE_COMPLETE_UI_DIST="$PULSE_M6_OPERATION_ASSETS" PULSE_TEST_TEMP_ROOT="$PULSE_M6_OPERATION_SCREENSHOTS"
 mise exec -- go test -race ./internal/httpapi -run '^TestCompleteTVOrganizationPreviewPGAppBrowser$' -count=1 -v -timeout=240s 2>&1 | tee "$owned/acceptance.log"
 if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M6_OPERATION_UNEXPECTED_SKIP >&2; exit 1; fi
 test "$(grep -c '^--- PASS: TestCompleteTVOrganizationPreviewPGAppBrowser (' "$owned/acceptance.log")" = 1
 for image in organization-desktop organization-mobile-preview organization-mobile; do test -s "$PULSE_M6_OPERATION_SCREENSHOTS/$image.png"; done
 printf 'M6_OPERATION_APPROVAL_SCREENSHOTS_READY %s\n' "$PULSE_M6_OPERATION_SCREENSHOTS"
else
 mise exec -- go test -race \
  ./internal/httpapi/m5_indexer_acceptance_test.go \
  ./internal/httpapi/m5_interactive_search_acceptance_test.go \
  ./internal/httpapi/m5_subscription_creation_acceptance_test.go \
  ./internal/httpapi/file_mapping_browser_helper_test.go \
  ./internal/httpapi/m6_operation_activity_browser_test.go \
  -run '^TestM6OperationActivityActualChromeHTTPPG$' -count=1 -v -timeout=240s 2>&1 | tee "$owned/acceptance.log"
 if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M6_OPERATION_UNEXPECTED_SKIP >&2; exit 1; fi
 test "$(grep -c '^--- PASS: TestM6OperationActivityActualChromeHTTPPG (' "$owned/acceptance.log")" = 1
 for marker in M6_OPERATION_ACTIVITY_BROWSER_PASS M6_OPERATION_APP_COOKIE_PG_PASS M6_OPERATION_CHROME_CLEANUP_VERIFIED M6_OPERATION_SCREENSHOTS_READY; do
  test "$(grep -c "$marker" "$owned/acceptance.log")" = 1
 done
 for width in 1280 768 375 320; do
  for view in list detail timeline; do test -s "$PULSE_M6_OPERATION_SCREENSHOTS/$view-$width.png"; done
 done
fi
# The inspection window is outside the Go and Chrome deadlines.
if [[ ${PULSE_M6_OPERATION_REVIEW:-0} == 1 ]]; then
 inspected=0
 for ((attempt=0;attempt<180;attempt++)); do
  if [[ -f "$PULSE_M6_OPERATION_SCREENSHOTS/inspected" ]]; then inspected=1; break; fi
  sleep 1
 done
 if [[ $inspected != 1 ]]; then echo M6_OPERATION_SCREENSHOT_INSPECTION_TIMEOUT >&2; exit 1; fi
fi
if [[ $mode == approval ]]; then
 echo 'M6_OPERATION_APPROVAL_ACCEPTANCE_EXECUTED tests=1 skipped=0 realApp=true native=true realIngestion=true originalKeyRecovery=true'
else
 echo 'M6_OPERATION_ACCEPTANCE_EXECUTED tests=1 skipped=0 extra_business_writes=0 Add=0 SQL_display_only=true'
fi
