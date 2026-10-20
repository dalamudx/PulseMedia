#!/usr/bin/env bash
# Main runs: mise exec -- bash scripts/verify_m8_site_account_stats_ui.sh
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m8_site_account_stats_ui.sh' >&2; exit 2; fi

# The outer owner survives the PG wrapper solely for screenshot review. No App,
# Chrome or PG is alive when READY is published. Inner invocation is not public.
if [[ ${PULSE_M8_STATS_UI_INNER:-0} == 1 ]]; then
 : "${PULSE_TEST_DATABASE_URL:?private PostgreSQL required}"
 : "${PULSE_M8_STATS_UI_OWNED:?outer owned directory required}"
 owned=$PULSE_M8_STATS_UI_OWNED
 case "$owned" in /data/temp/pulse-m8-stats-ui.*) test -d "$owned" && test ! -L "$owned";; *) exit 98;; esac
 export TMPDIR="$owned/run" TMP="$owned/run" TEMP="$owned/run" GOTMPDIR="$owned/run/build"
 export XDG_CACHE_HOME="$owned/run/cache" XDG_CONFIG_HOME="$owned/run/config"
 export PULSE_M8_STATS_UI=1 PULSE_M8_STATS_UI_REPO="$repo"
 export PULSE_M8_STATS_UI_ASSETS="$owned/web" PULSE_M8_STATS_UI_SHOTS="$owned/screenshots"
 mkdir -p "$GOTMPDIR" "$PULSE_M8_STATS_UI_SHOTS"
 cat > "$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const env=process.env,load=createRequire(env.PULSE_M8_STATS_UI_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
export default {root:env.PULSE_M8_STATS_UI_REPO+'/web',envDir:env.TMPDIR,plugins:[react()],cacheDir:env.TMPDIR+'/vite-cache',resolve:{alias:{'@':env.PULSE_M8_STATS_UI_REPO+'/web/src'}},build:{outDir:env.PULSE_M8_STATS_UI_ASSETS,emptyOutDir:true}};
CONFIG
 timeout --signal=TERM --kill-after=10s 120s pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/web-build.log" 2>&1 || { echo 'M8_STATS_UI_BUILD_FAILED phase=vite' >&2; exit 1; }
 go test -race -count=1 -timeout=3m ./internal/app -run '^TestM8SiteAccountStatsUI$' -v 2>&1 | tee "$owned/acceptance.log"
 if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M8_STATS_UI_UNEXPECTED_SKIP >&2; exit 1; fi
 test "$(grep -c '^--- PASS: TestM8SiteAccountStatsUI (' "$owned/acceptance.log")" = 1
 test "$(grep -c '^--- PASS:' "$owned/acceptance.log")" = 1
 for marker in M8_STATS_UI_BROWSER_PASS M8_STATS_UI_CHROME_CLEANUP_VERIFIED M8_STATS_UI_APP_PASS; do
  test "$(grep -c "$marker" "$owned/acceptance.log")" = 1
 done
 exit 0
fi

owned=$(mktemp -d /data/temp/pulse-m8-stats-ui.XXXXXX)
cleanup(){
 rc=$?
 trap - EXIT
 case "$owned" in /data/temp/pulse-m8-stats-ui.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M8_STATS_UI_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export PULSE_M8_STATS_UI_OWNED="$owned"
# with_podman_test_postgres owns and cleans its distinct private PG18 directory.
# It must return successfully, including cleanup, before publishing the review event.
env PULSE_M8_STATS_UI_INNER=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m8_site_account_stats_ui.sh
for file in fresh-375.png fresh-320.png fresh-768.png fresh-1440.png unknown-320.png stale-375.png; do
 test -s "$owned/screenshots/$file"
done
# Register the review marker BEFORE the wake line. Main reads these images, then
# creates exactly $owned/REVIEWED (regular file, mode 0600) within ten minutes.
{
 printf 'REVIEWED=%s/REVIEWED\n' "$owned"
 for file in fresh-375.png fresh-320.png fresh-768.png fresh-1440.png unknown-320.png stale-375.png; do
  printf '%s/screenshots/%s\n' "$owned" "$file"
 done
} > "$owned/READY"
chmod 0600 "$owned/READY"
printf 'M8_STATS_UI_SCREENSHOTS_READY READY=%s/READY REVIEWED=%s/REVIEWED\n' "$owned" "$owned"
for file in fresh-375.png fresh-320.png fresh-768.png fresh-1440.png unknown-320.png stale-375.png; do
 printf 'M8_STATS_UI_SCREENSHOT %s/screenshots/%s\n' "$owned" "$file"
done
reviewed=0
for ((attempt=0;attempt<600;attempt++)); do
 if [[ -f "$owned/REVIEWED" && ! -L "$owned/REVIEWED" && $(stat -c '%a' "$owned/REVIEWED") == 600 ]]; then reviewed=1; break; fi
 sleep 1
done
if [[ $reviewed != 1 ]]; then echo M8_STATS_UI_REVIEW_TIMEOUT >&2; exit 1; fi
echo 'M8_STATS_UI_ACCEPTANCE_EXECUTED parents=1 skipped=0 actual_App=true real_Cookie=true private_PG18=true native_fresh=true semantic_models=separate screenshots_reviewed=true'
