#!/usr/bin/env bash
# Settings command acceptance against an owned PostgreSQL 18 cluster.
set -euo pipefail
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-target}
case "$mode" in target|full|remaining) ;; *) echo "Usage: $0 [target|full|remaining]" >&2;exit 2;;esac
mkdir -p /data/temp
owned=$(mktemp -d /data/temp/pulse-settings-transaction.XXXXXX)
pgbin=${PULSE_SETTINGS_TEST_PG_BIN:-"$(mise where postgres)/bin"}
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" PULSE_UI_FIXTURE=0
export PULSE_TEST_DATABASE_URL="postgres://postgres@localhost/postgres?host=$owned/socket&port=5432&sslmode=disable"
cleanup(){ rc=$?;trap - EXIT;if runuser -u postgres -- "$pgbin/pg_ctl" -D "$owned/pg" status >/dev/null 2>&1;then runuser -u postgres -- "$pgbin/pg_ctl" -D "$owned/pg" -m fast -w stop >"$owned/stop.log" 2>&1 || { echo "Owned cluster cleanup blocked at $owned";exit 1; };fi;case "$owned" in /data/temp/pulse-settings-transaction.*) rm -rf -- "$owned";;*) exit 1;;esac;test ! -e "$owned" && echo OWNED_TEMP_CLEANUP_VERIFIED;exit "$rc";}
trap cleanup EXIT
expected_version=$(mise exec -- postgres --version)
test "$("$pgbin/postgres" --version)" = "$expected_version"
runuser -u postgres -- test -x "$pgbin/initdb"
mkdir -p "$owned/build" "$owned/socket" "$owned/pg";chmod 755 "$owned";chown postgres:postgres "$owned/socket" "$owned/pg"
runuser -u postgres -- "$pgbin/initdb" -D "$owned/pg" --auth=trust --no-locale >"$owned/init.log" 2>&1
runuser -u postgres -- "$pgbin/pg_ctl" -D "$owned/pg" -o "-k $owned/socket -c listen_addresses='' -c max_connections=100" -w start >"$owned/start.log" 2>&1
if [ "$mode" != remaining ];then
 mise exec -- go test -race ./internal/usecase/settings ./internal/command ./internal/httpapi -run 'TestSetting|TestSettings|TestTMDBSetting|TestAPICredential|TestAPI.*Scope' -count=3 >"$owned/target.log" 2>&1 || { sed -n '/--- FAIL/,+12p;/build failed/,+3p' "$owned/target.log";tail -n 20 "$owned/target.log";exit 1; }
 echo SETTINGS_FAULT_CANCEL_LEGACY_ONCE_ONLY_RACE_PASS
fi
if [ "$mode" = full ];then
 mise exec -- task gen-check >"$owned/gen.log" 2>&1 || { tail -n 40 "$owned/gen.log";exit 1; }
 mise exec -- task test-web >"$owned/types.log" 2>&1 || { tail -n 35 "$owned/types.log";exit 1; }
 echo GEN_TYPECHECK_PASS
 mise exec -- go test -p 2 ./... -count=1 >"$owned/all.log" 2>&1 || { sed -n '/--- FAIL/,+14p' "$owned/all.log";tail -n 55 "$owned/all.log";exit 1; }
 echo ALL_PASS
fi
if [ "$mode" = full ] || [ "$mode" = remaining ];then
 mise exec -- go test -race -p 2 ./internal/app ./internal/httpapi ./internal/store/postgres ./internal/usecase/settings ./internal/usecase/auth ./internal/command ./internal/ports -count=1 >"$owned/race.log" 2>&1 || { sed -n '/--- FAIL/,+14p' "$owned/race.log";tail -n 55 "$owned/race.log";exit 1; }
 echo RACE_PASS
fi
remaining=$("$pgbin/psql" -h "$owned/socket" -U postgres -d postgres -Atc "SELECT count(*) FROM pg_database WHERE datname NOT IN ('postgres','template0','template1')")
test "$remaining" = 0 && echo OWNED_DATABASE_CLEANUP_VERIFIED
