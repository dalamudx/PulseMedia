#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [ "$#" -ne 0 ] || [ "${PULSE_M6_SHELL_BROWSER:-}" != 1 ]; then echo 'Set PULSE_M6_SHELL_BROWSER=1; verify_m6_shell_ui.sh takes no arguments.' >&2; exit 2; fi
if [ "${PULSE_M6_SHELL_OWNED_PG:-}" != 1 ]; then
 exec env PULSE_M6_SHELL_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m6_shell_ui.sh
fi
: "${PULSE_TEST_DATABASE_URL:?private PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m6-shell.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m6-shell.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M6_SHELL_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache"
export XDG_CACHE_HOME="$owned/cache" XDG_CONFIG_HOME="$owned/config"
export PULSE_M6_SHELL_REPO="$repo" PULSE_M6_SHELL_ASSETS="$owned/web" PULSE_M6_SHELL_SCREENSHOTS="$owned/screenshots"
mkdir -p "$GOTMPDIR" "$PULSE_M6_SHELL_SCREENSHOTS"
cat > "$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const load=createRequire(process.env.PULSE_M6_SHELL_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
export default {
 root:process.env.PULSE_M6_SHELL_REPO+'/web',envDir:process.env.TMPDIR,plugins:[react()],cacheDir:process.env.TMPDIR+'/vite-cache',
 resolve:{alias:{'@':process.env.PULSE_M6_SHELL_REPO+'/web/src'}},
 build:{outDir:process.env.PULSE_M6_SHELL_ASSETS,emptyOutDir:true}
};
CONFIG
mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/web-build.log" 2>&1 || { tail -n 30 "$owned/web-build.log"; exit 1; }
mise exec -- go test -race ./internal/app -run '^TestM6ApplicationShellActualChromeHTTPPG$' -count=1 -v -timeout=240s 2>&1 | tee "$owned/acceptance.log"
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M6_SHELL_UNEXPECTED_SKIP >&2; exit 1; fi
test "$(grep -c '^--- PASS: TestM6ApplicationShellActualChromeHTTPPG' "$owned/acceptance.log")" = 1
for marker in M6_SHELL_BROWSER_PASS M6_SHELL_APP_COOKIE_PG_PASS M6_SHELL_CHROME_CLEANUP_VERIFIED; do grep -q "$marker" "$owned/acceptance.log"; done
for name in shell-desktop shell-tablet shell-375 shell-drawer-375; do test -s "$PULSE_M6_SHELL_SCREENSHOTS/$name.png"; done
if [ "${PULSE_M6_SHELL_REVIEW:-}" = 1 ]; then test -f "$PULSE_M6_SHELL_SCREENSHOTS/inspected"; fi
echo 'M6_SHELL_ACCEPTANCE_EXECUTED tests=1 skipped=0'
