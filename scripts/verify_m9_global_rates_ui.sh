#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ ${PULSE_M9_RATE_BROWSER:-} != 1 ]]; then echo 'Set PULSE_M9_RATE_BROWSER=1 for owned Chrome/PostgreSQL acceptance.' >&2; exit 2; fi
if [[ ${PULSE_M9_RATE_OWNED_PG:-} != 1 ]]; then exec env PULSE_M9_RATE_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m9_global_rates_ui.sh; fi
: "${PULSE_TEST_DATABASE_URL:?private PostgreSQL required}"
test_case=TestM9GlobalRatesNativeChromePG
if [[ ${PULSE_M9_HOME_INBOX_BROWSER:-} == 1 ]]; then test_case=TestM9HomeInboxNativeChromePG; fi
owned=$(mktemp -d /data/temp/pulse-m9-rate-browser.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m9-rate-browser.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M9_GLOBAL_RATE_CHROME_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache" XDG_CACHE_HOME="$owned/cache" XDG_CONFIG_HOME="$owned/config"
export PULSE_M9_RATE_REPO="$repo" PULSE_M9_RATE_ASSETS="$owned/web" PULSE_M9_RATE_SCREENSHOTS="$owned/screenshots"
mkdir -p "$GOTMPDIR" "$PULSE_M9_RATE_SCREENSHOTS"
cat > "$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const load=createRequire(process.env.PULSE_M9_RATE_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
let bridges=0;
const authBridge={name:'m9-owned-auth-status-observer',enforce:'pre',transform(source,id){
 if(!id.endsWith('/features/auth/AuthContext.tsx'))return null;
 const anchor='return <AuthContext.Provider';if(source.split(anchor).length!==2)throw Error('AuthContext bridge anchor changed');bridges++;
 return source.replace(anchor,'window.__m9RatesAuth={checkStatus};\n  '+anchor);
},closeBundle(){if(bridges!==1)throw Error('Expected one real AuthContext observer');}};
export default {root:process.env.PULSE_M9_RATE_REPO+'/web',envDir:process.env.TMPDIR,plugins:[authBridge,react()],cacheDir:process.env.TMPDIR+'/vite-cache',resolve:{alias:{'@':process.env.PULSE_M9_RATE_REPO+'/web/src'}},build:{outDir:process.env.PULSE_M9_RATE_ASSETS,emptyOutDir:true}};
CONFIG
mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" > "$owned/build.log" 2>&1 || { tail -n 30 "$owned/build.log"; exit 1; }
mise exec -- go test -race ./internal/app -run "^${test_case}$" -count=1 -v -timeout=7m 2>&1 | tee "$owned/acceptance.log"
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M9_RATE_BROWSER_UNEXPECTED_SKIP >&2; exit 1; fi
test "$(grep -c "^--- PASS: ${test_case} (" "$owned/acceptance.log")" = 1
if [[ ${PULSE_M9_HOME_INBOX_BROWSER:-} == 1 ]]; then
 names=(home-inbox-1440 home-inbox-768 home-inbox-375 home-inbox-320 home-inbox-pending home-inbox-failure)
else
 names=(global-rates-1440 global-rates-768 global-rates-375 global-rates-320 global-rates-failure global-rates-expired)
fi
for name in "${names[@]}"; do test -s "$PULSE_M9_RATE_SCREENSHOTS/$name.png"; done
if [[ ${PULSE_M9_RATE_REVIEW:-} == 1 ]]; then test -s "$PULSE_M9_RATE_SCREENSHOTS/inspected"; fi
echo M9_GLOBAL_RATE_NATIVE_CHROME_ACCEPTANCE_PASS_parents=1_skipped=0