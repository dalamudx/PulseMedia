#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
: "${PULSE_M9_EDITOR_BROWSER:?set to 1 for owned editor browser acceptance}"
if [[ ${PULSE_M9_EDITOR_OWNED_PG:-} != 1 ]]; then exec env PULSE_M9_EDITOR_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m9_configuration_editors_browser.sh; fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m9-editor-browser.XXXXXX)
cleanup(){ rc=$?; trap - EXIT; case "$owned" in /data/temp/pulse-m9-editor-browser.*) test ! -L "$owned"; test "$(realpath -- "$owned")" = "$owned"; rm -rf -- "$owned";; *) exit 98;; esac; test ! -e "$owned" || exit 1; echo M9_EDITOR_BROWSER_OWNED_TEMP_CLEANUP_VERIFIED; exit "$rc"; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" XDG_CACHE_HOME="$owned/cache" XDG_CONFIG_HOME="$owned/config"
export PULSE_M9_EDITOR_REPO="$repo" PULSE_M9_EDITOR_ASSETS="$owned/web" PULSE_M9_EDITOR_SCREENSHOTS="$owned/screenshots" PULSE_M9_EDITOR_REVIEW="${PULSE_M9_EDITOR_REVIEW:-1}"
mkdir -p "$GOTMPDIR" "$PULSE_M9_EDITOR_SCREENSHOTS"
cat >"$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const load=createRequire(process.env.PULSE_M9_EDITOR_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
export default {root:process.env.PULSE_M9_EDITOR_REPO+'/web',envDir:process.env.TMPDIR,plugins:[react()],cacheDir:process.env.TMPDIR+'/vite-cache',resolve:{alias:{'@':process.env.PULSE_M9_EDITOR_REPO+'/web/src'}},build:{outDir:process.env.PULSE_M9_EDITOR_ASSETS,emptyOutDir:true}};
CONFIG
mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/build.log" 2>&1 || { tail -n 35 "$owned/build.log"; exit 1; }
mise exec -- go test -race ./internal/app -run '^TestM9ConfigurationEditorsChromePG$' -count=1 -v -timeout=10m 2>&1 | tee "$owned/acceptance.log"
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then exit 1; fi
test "$(grep -c '^--- PASS: TestM9ConfigurationEditorsChromePG (' "$owned/acceptance.log")" = 1
test "$(grep -c '^M9_EDITOR_CHROME_PASS checks=' "$owned/acceptance.log")" = 1
for name in editor-naming-1440 editor-naming-768 editor-naming-375 editor-naming-320 editor-words editor-recovery; do test -s "$PULSE_M9_EDITOR_SCREENSHOTS/$name.png"; done
if [[ "$PULSE_M9_EDITOR_REVIEW" == 1 ]]; then test -f "$PULSE_M9_EDITOR_SCREENSHOTS/inspected"; fi
echo M9_EDITOR_NATIVE_CHROME_ACCEPTANCE_PASS_parents=1_skipped=0
