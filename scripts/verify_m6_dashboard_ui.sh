#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# -gt 1 || ${PULSE_M6_DASHBOARD_BROWSER:-} != 1 ]]; then
 echo 'Set PULSE_M6_DASHBOARD_BROWSER=1; usage: verify_m6_dashboard_ui.sh [all|states|recovery]' >&2; exit 2
fi
mode=${1:-all}
case "$mode" in all|states|recovery) ;; *) echo 'Expected all, states or recovery.' >&2; exit 2;; esac
if [[ ${PULSE_M6_DASHBOARD_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M6_DASHBOARD_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m6_dashboard_ui.sh "$mode"
fi
: "${PULSE_TEST_DATABASE_URL:?private PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m6-dashboard.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m6-dashboard.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M6_DASHBOARD_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache"
export XDG_CACHE_HOME="$owned/cache" XDG_CONFIG_HOME="$owned/config"
export PULSE_M6_DASHBOARD_REPO="$repo" PULSE_M6_DASHBOARD_ASSETS="$owned/web" PULSE_M6_DASHBOARD_SCREENSHOTS="$owned/screenshots"
mkdir -p "$GOTMPDIR" "$PULSE_M6_DASHBOARD_SCREENSHOTS"
cat > "$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const load=createRequire(process.env.PULSE_M6_DASHBOARD_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
let bridges=0;
// Private build instrumentation exposes the real AuthContext status recheck.
// It cannot set a user or role; the production Cookie endpoint decides both.
const authBridge={name:'m6-dashboard-private-auth-observer',enforce:'pre',
 transform(source,id){
  if(!id.endsWith('/features/auth/AuthContext.tsx'))return null;
  const anchor='return <AuthContext.Provider';
  if(source.split(anchor).length!==2)throw new Error('AuthContext bridge anchor changed');
  bridges++;
  return source.replace(anchor,'window.__m6DashboardAuth = {checkStatus};\n  '+anchor);
 },
 closeBundle(){if(bridges!==1)throw new Error('Expected exactly one private AuthContext bridge');}
};
export default {
 root:process.env.PULSE_M6_DASHBOARD_REPO+'/web',envDir:process.env.TMPDIR,plugins:[authBridge,react()],cacheDir:process.env.TMPDIR+'/vite-cache',
 resolve:{alias:{'@':process.env.PULSE_M6_DASHBOARD_REPO+'/web/src'}},
 build:{outDir:process.env.PULSE_M6_DASHBOARD_ASSETS,emptyOutDir:true}
};
CONFIG
mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/web-build.log" 2>&1 || { tail -n 30 "$owned/web-build.log"; exit 1; }
case "$mode" in
 all) pattern='^TestM6Dashboard(States|Recovery)ActualChromeHTTPPG$'; modes=(states recovery);;
 states) pattern='^TestM6DashboardStatesActualChromeHTTPPG$'; modes=(states);;
 recovery) pattern='^TestM6DashboardRecoveryActualChromeHTTPPG$'; modes=(recovery);;
esac
# Foreground pipeline waits for terminal test/Chrome cleanup. The owned Chrome
# helper kills its process group and waits before go test exits, including failure.
mise exec -- go test -race ./internal/app -run "$pattern" -count=1 -v -timeout=480s 2>&1 | tee "$owned/acceptance.log"
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M6_DASHBOARD_UNEXPECTED_SKIP >&2; exit 1; fi
expected=${#modes[@]}
test "$(grep -c '^--- PASS: TestM6Dashboard.*ActualChromeHTTPPG (' "$owned/acceptance.log")" = "$expected"
for selected in "${modes[@]}"; do
 case "$selected" in states) test_name=TestM6DashboardStatesActualChromeHTTPPG;; recovery) test_name=TestM6DashboardRecoveryActualChromeHTTPPG;; esac
 test "$(grep -c "^--- PASS: $test_name (" "$owned/acceptance.log")" = 1
 for marker in M6_DASHBOARD_BROWSER_PASS M6_DASHBOARD_APP_COOKIE_PG_PASS M6_DASHBOARD_CHROME_CLEANUP_VERIFIED; do
  test "$(grep -c "$marker $selected" "$owned/acceptance.log")" = 1
 done
 for name in dashboard-desktop dashboard-tablet dashboard-375 dashboard-320; do test -s "$PULSE_M6_DASHBOARD_SCREENSHOTS/$selected/$name.png"; done
 if [[ ${PULSE_M6_DASHBOARD_REVIEW:-} == 1 && $selected == states ]]; then test -f "$PULSE_M6_DASHBOARD_SCREENSHOTS/$selected/inspected"; fi
done
echo "M6_DASHBOARD_ACCEPTANCE_EXECUTED tests=$expected skipped=0 mode=$mode source=SQL_prepared_observations"
