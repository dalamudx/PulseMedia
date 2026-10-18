#!/usr/bin/env bash
# Run: mise exec -- bash scripts/verify_m5_subscription_creation.sh [http|browser|all]
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-all}
case "$mode" in http|browser|all) ;; *) echo 'Usage: verify_m5_subscription_creation.sh [http|browser|all]' >&2; exit 2;; esac
# Both test opt-ins precede every database; this wrapper always owns its PG18.6.
export PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE=1
if [ "$mode" != http ]; then export PULSE_M5_SUBSCRIPTION_CREATION_BROWSER=1; else export PULSE_M5_SUBSCRIPTION_CREATION_BROWSER=0; fi
if [ "${PULSE_M5_CREATION_OWNED_PG:-}" != 1 ]; then
 exec env PULSE_M5_CREATION_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m5_subscription_creation.sh "$mode"
fi
: "${PULSE_TEST_DATABASE_URL:?private PostgreSQL fixture required}"
owned=$(mktemp -d /data/temp/pulse-m5-creation.XXXXXX)
cleanup(){
 rc=$?
 trap - EXIT
 # Interactive screenshot inspection is bounded and all artifacts are still deleted.
 if [ "${PULSE_M5_CREATION_INSPECT_SCREENSHOTS:-}" = 1 ] && [ -d "$owned/screenshots" ]; then
  echo "M5_CREATION_SCREENSHOTS_READY $owned/screenshots"
  inspected=0
  for ((attempt=0;attempt<120;attempt++)); do
   if [ -f "$owned/screenshots/inspected" ]; then inspected=1; break; fi
   sleep 1
  done
  if [ "$inspected" != 1 ]; then echo M5_CREATION_SCREENSHOT_INSPECTION_TIMEOUT >&2; rc=1; fi
 fi
 case "$owned" in /data/temp/pulse-m5-creation.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M5_CREATION_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build"
export PULSE_M5_CREATION_REPO="$repo" PULSE_M5_CREATION_ASSETS="$owned/built-web" PULSE_M5_CREATION_SCREENSHOTS="$owned/screenshots"
mkdir -p "$GOTMPDIR" "$PULSE_M5_CREATION_SCREENSHOTS"
if [ "$mode" != http ]; then
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
 mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/web-build.log" 2>&1 || { tail -n 25 "$owned/web-build.log"; exit 1; }
fi
pattern='^TestM5SubscriptionCreation.*HTTPPG$'
if [ "$mode" = http ]; then pattern='^TestM5SubscriptionCreation(NativeSparseRecovery|StrictValidation|NativeFailuresRollback|CurrentAccountFences|RootStorageFailure)HTTPPG$'; fi
if [ "$mode" = browser ]; then pattern='^TestM5SubscriptionCreationActualChromeHTTPPG$'; fi
if ! mise exec -- go test -race ./internal/httpapi/m5_indexer_acceptance_test.go ./internal/httpapi/m5_interactive_search_acceptance_test.go ./internal/httpapi/m5_subscription_creation_acceptance_test.go ./internal/httpapi/m5_subscription_creation_browser_test.go -run "$pattern" -count=1 -v -timeout=8m >"$owned/acceptance.log" 2>&1; then
 tail -n 75 "$owned/acceptance.log"; exit 1
fi
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M5_CREATION_UNEXPECTED_SKIP >&2; exit 1; fi
expected=6
if [ "$mode" = http ]; then expected=5; elif [ "$mode" = browser ]; then expected=1; fi
actual=$(grep -c '^--- PASS: TestM5SubscriptionCreation' "$owned/acceptance.log")
if [ "$actual" != "$expected" ]; then echo "M5_CREATION_MISSING_EXECUTION expected=$expected actual=$actual" >&2; exit 1; fi
if [ "$mode" != http ]; then grep -q M5_SUBSCRIPTION_CREATION_ACTUAL_CHROME_PASS "$owned/acceptance.log" || exit 1; fi
grep -E 'M5_SUBSCRIPTION_CREATION_|--- PASS:|^ok[[:space:]]' "$owned/acceptance.log"
echo "M5_CREATION_ACCEPTANCE_EXECUTED mode=$mode tests=$actual"
