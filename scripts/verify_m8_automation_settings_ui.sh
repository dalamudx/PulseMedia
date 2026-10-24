#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m8_automation_settings_ui.sh' >&2; exit 2; fi
if [[ ${PULSE_M8_AUTOMATION_UI_INNER:-0} == 1 ]]; then
 : "${PULSE_TEST_DATABASE_URL:?private PostgreSQL required}"
 : "${PULSE_M8_AUTOMATION_UI_OWNED:?owned outer directory required}"
 owned=$PULSE_M8_AUTOMATION_UI_OWNED
 case "$owned" in /data/temp/pulse-m8-automation-ui.*) test -d "$owned" && test ! -L "$owned";; *) exit 98;; esac
 export TMPDIR="$owned/run" TMP="$owned/run" TEMP="$owned/run" GOTMPDIR="$owned/run/build"
 export XDG_CACHE_HOME="$owned/run/cache" XDG_CONFIG_HOME="$owned/run/config"
 export PULSE_M8_AUTOMATION_UI=1 PULSE_M8_AUTOMATION_UI_REPO="$repo"
 export PULSE_M8_AUTOMATION_UI_ASSETS="$owned/web" PULSE_M8_AUTOMATION_UI_SHOTS="$owned/screenshots"
 mkdir -p "$GOTMPDIR" "$PULSE_M8_AUTOMATION_UI_SHOTS"
 cat > "$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const env=process.env,load=createRequire(env.PULSE_M8_AUTOMATION_UI_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
export default {root:env.PULSE_M8_AUTOMATION_UI_REPO+'/web',envDir:env.TMPDIR,plugins:[react()],cacheDir:env.TMPDIR+'/vite-cache',resolve:{alias:{'@':env.PULSE_M8_AUTOMATION_UI_REPO+'/web/src'}},build:{outDir:env.PULSE_M8_AUTOMATION_UI_ASSETS,emptyOutDir:true}};
CONFIG
 timeout --signal=TERM --kill-after=10s 120s pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/web-build.log" 2>&1 || { echo 'M8_AUTOMATION_UI_BUILD_FAILED' >&2; exit 1; }
 go test -race -count=1 -timeout=6m ./internal/app -run '^TestM8AutomationSettingsUI$' -v 2>&1 | tee "$owned/acceptance.log"
 if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M8_AUTOMATION_UI_UNEXPECTED_SKIP >&2; exit 1; fi
 test "$(grep -c '^--- PASS: TestM8AutomationSettingsUI (' "$owned/acceptance.log")" = 1
 test "$(grep -c '^--- PASS:' "$owned/acceptance.log")" = 1
 for marker in M8_AUTOMATION_UI_BROWSER_PASS M8_AUTOMATION_UI_CHROME_CLEANUP_VERIFIED M8_AUTOMATION_UI_APP_PASS; do test "$(grep -c "$marker" "$owned/acceptance.log")" = 1; done
 exit 0
fi
owned=$(mktemp -d /data/temp/pulse-m8-automation-ui.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m8-automation-ui.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M8_AUTOMATION_UI_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export PULSE_M8_AUTOMATION_UI_OWNED="$owned"
env PULSE_M8_AUTOMATION_UI_INNER=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m8_automation_settings_ui.sh
for file in douban-320.png douban-375.png douban-768.png douban-1440.png checkin-375.png checkin-1440.png checkin-source-change-375.png; do test -s "$owned/screenshots/$file"; done
{
 printf 'REVIEWED=%s/REVIEWED\n' "$owned"
 for file in douban-320.png douban-375.png douban-768.png douban-1440.png checkin-375.png checkin-1440.png checkin-source-change-375.png; do printf '%s/screenshots/%s\n' "$owned" "$file"; done
} > "$owned/READY"
chmod 0600 "$owned/READY"
printf 'M8_AUTOMATION_UI_SCREENSHOTS_READY READY=%s/READY REVIEWED=%s/REVIEWED\n' "$owned" "$owned"
reviewed=0
for ((attempt=0;attempt<600;attempt++)); do
 if [[ -f "$owned/REVIEWED" && ! -L "$owned/REVIEWED" && $(stat -c '%a' "$owned/REVIEWED") == 600 ]]; then reviewed=1; break; fi
 sleep 1
done
if [[ $reviewed != 1 ]]; then echo M8_AUTOMATION_UI_REVIEW_TIMEOUT >&2; exit 1; fi
echo 'M8_AUTOMATION_SETTINGS_UI_ACCEPTANCE_PASS parents=1 skipped=0 App=REAL Cookie=REAL PG=18.6 configuration_receipts=REAL upstream_calls=0 protected_public_tables=UNCHANGED widths=320_375_768_1440 screenshots_reviewed=true'
