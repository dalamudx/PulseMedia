#!/usr/bin/env bash
# Reproducible member account UI acceptance on a private PostgreSQL cluster.
set -euo pipefail
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-browser}
case "$mode" in browser|full|remaining|settings|auth|auth-full) ;; *) echo "Usage: $0 [browser|full|remaining|settings|auth|auth-full]" >&2; exit 2;; esac
mkdir -p /data/temp
owned=$(mktemp -d /data/temp/pulse-account-token-browser.XXXXXX)
pgbin=${PULSE_ACCOUNT_TEST_PG_BIN:-"$(mise where postgres)/bin"}
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build"
export PULSE_TOKEN_UI_REPO="$repo" PULSE_TOKEN_UI_TASK_ROOT="$owned"
export PULSE_TEST_DATABASE_URL="postgres://postgres@localhost/postgres?host=$owned/socket&port=5432&sslmode=disable"
export PULSE_UI_FIXTURE=0
swapped=0
run_pg(){ if [ "$(id -u)" = 0 ]; then runuser -u postgres -- "$@"; else "$@"; fi; }
cleanup() {
 rc=$?
 trap - EXIT
 if run_pg "$pgbin/pg_ctl" -D "$owned/pg" status >/dev/null 2>&1; then
  if ! run_pg "$pgbin/pg_ctl" -D "$owned/pg" -m fast -w stop >"$owned/stop.log" 2>&1; then
   cat "$owned/stop.log"; echo "Owned cluster remains at $owned"; exit 1
  fi
 fi
 if [ "$swapped" -eq 1 ]; then
  if [ -f "$repo/web/dist/.account-ui-owned" ] && [ "$(cat "$repo/web/dist/.account-ui-owned")" = "$owned" ]; then
   rm -rf -- "$repo/web/dist"
   if [ -d "$owned/original-dist" ]; then mv -- "$owned/original-dist" "$repo/web/dist"; fi
   echo ORIGINAL_FRONTEND_ASSETS_RESTORED
  else echo "Build ownership mismatch; retained $owned"; exit 1; fi
 fi
 case "$owned" in /data/temp/pulse-account-token-browser.*) rm -rf -- "$owned";; *) exit 1;; esac
 test ! -e "$owned" && echo OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
expected_version=$(mise exec -- postgres --version)
test "$("$pgbin/postgres" --version)" = "$expected_version"
run_pg test -x "$pgbin/initdb"
mkdir -p "$GOTMPDIR" "$owned/socket" "$owned/pg"
chmod 755 "$owned"
if [ "$(id -u)" = 0 ]; then chown postgres:postgres "$owned/socket" "$owned/pg"; fi
run_pg "$pgbin/initdb" -D "$owned/pg" -U postgres -A trust --no-locale --encoding=UTF8 >"$owned/init.log" 2>&1 || { tail -n 20 "$owned/init.log"; exit 1; }
run_pg "$pgbin/pg_ctl" -D "$owned/pg" -l "$owned/pg/server.log" -o "-c listen_addresses='' -c unix_socket_directories='$owned/socket' -p 5432" -w start
cat > "$owned/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const load=createRequire(process.env.PULSE_TOKEN_UI_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
export default {
 root:process.env.PULSE_TOKEN_UI_REPO+'/web',
 envDir:process.env.PULSE_TOKEN_UI_TASK_ROOT,
 plugins:[react()],
 resolve:{alias:{'@':process.env.PULSE_TOKEN_UI_REPO+'/web/src'}},
 build:{outDir:process.env.PULSE_TOKEN_UI_TASK_ROOT+'/built-web',emptyOutDir:true}
};
CONFIG
mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" >"$owned/web-build.log" 2>&1 || { tail -n 30 "$owned/web-build.log"; exit 1; }
if [ -d "$repo/web/dist" ]; then mv -- "$repo/web/dist" "$owned/original-dist"; fi
mkdir "$repo/web/dist"
printf '%s' "$owned" > "$repo/web/dist/.account-ui-owned"
swapped=1
cp -a "$owned/built-web/." "$repo/web/dist/"
if [ "$mode" = "auth" ] || [ "$mode" = "auth-full" ];then
 PULSE_UI_FIXTURE=1 mise exec -- go test -race ./internal/httpapi -run "^(Test(AuthLifecycleBrowser|AuthStatusDatabaseFailure|AccountSecurityActualChrome)HTTPPG|TestMemberPasswordResetActualRouterPG|TestMFARecoveryRegenerationActualRouterPG|TestRevokeOtherSessionsActualRouterPG)$" -count=1 -v >"$owned/browser.log" 2>&1 || { grep -n -B12 -A3 -E -- '--- FAIL:|Error:|panic:' "$owned/browser.log" || true; tail -n 40 "$owned/browser.log"; exit 1; }
 test "$(grep -c '^--- PASS:' "$owned/browser.log")" = 6 || { echo AUTH_ACCEPTANCE_PARENT_COUNT_MISMATCH; exit 1; }
 if grep -q -- '--- SKIP:' "$owned/browser.log"; then echo AUTH_ACCEPTANCE_SKIPPED; exit 1; fi
 sed -n "/passed.*realPG/p;/--- PASS/p" "$owned/browser.log"
 echo AUTH_LIFECYCLE_UI_PG_BROWSER_PASS
elif [ "$mode" = "settings" ];then
 PULSE_UI_FIXTURE=1 mise exec -- go test -race ./internal/httpapi -run "^TestSettingsCommandBrowserHTTPPG$" -count=1 -v >"$owned/browser.log" 2>&1 || { tail -n 55 "$owned/browser.log";exit 1; }
 sed -n "/passed.*realPG/p;/--- PASS/p" "$owned/browser.log"
 echo SETTINGS_COMMAND_UI_PG_BROWSER_PASS
elif [ "$mode" != "remaining" ]; then
 mise exec -- go test -race ./internal/httpapi -run '^(TestExpiredTokenOriginalReceiptRecoveryHTTPPG|TestOwnTokenManagementHTTPPG)$' -count=3 >"$owned/target.log" 2>&1 || { tail -n 45 "$owned/target.log"; exit 1; }
 echo EXPIRY_RECOVERY_AND_OWN_TOKEN_PG_RACE_PASS
 PULSE_UI_FIXTURE=1 mise exec -- go test -race ./internal/httpapi -run '^TestAccountTokenBrowserHTTPPG$' -count=1 -v >"$owned/browser.log" 2>&1 || { tail -n 55 "$owned/browser.log"; exit 1; }
 sed -n '/passed.*realPG/p;/--- PASS/p' "$owned/browser.log"
 echo MEMBER_ACCOUNT_UI_PG_BROWSER_PASS
fi
if [ "$mode" = "full" ] || [ "$mode" = "auth-full" ]; then
 mise exec -- task gen-check >"$owned/gen.log" 2>&1 || { tail -n 40 "$owned/gen.log"; exit 1; }
 mise exec -- task test-web >"$owned/types.log" 2>&1 || { tail -n 35 "$owned/types.log"; exit 1; }
 echo GEN_TYPECHECK_PASS
 mise exec -- go test -p 2 ./... -count=1 >"$owned/all.log" 2>&1 || { tail -n 85 "$owned/all.log"; exit 1; }
 echo ALL_PASS
elif [ "$mode" = "remaining" ]; then
 mise exec -- go test ./internal/httpapi -count=1 >"$owned/http.log" 2>&1 || { tail -n 85 "$owned/http.log"; exit 1; }
 echo FAILED_HTTP_PACKAGE_PASS
fi
if [ "$mode" = "full" ] || [ "$mode" = "remaining" ] || [ "$mode" = "auth-full" ]; then
 mise exec -- go test -race -p 2 ./internal/app ./internal/httpapi ./internal/store/postgres ./internal/usecase/auth ./internal/usecase/settings ./internal/command ./internal/ports -count=1 >"$owned/race.log" 2>&1 || { tail -n 85 "$owned/race.log"; exit 1; }
 echo RACE_PASS
fi
remaining=$("$pgbin/psql" -h "$owned/socket" -U postgres -d postgres -Atc "SELECT count(*) FROM pg_database WHERE datname NOT IN ('postgres','template0','template1')")
test "$remaining" = 0
echo OWNED_DATABASE_CLEANUP_VERIFIED
