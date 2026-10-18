#!/usr/bin/env bash
# Run a command against a private PostgreSQL from the pinned mise toolchain.
set -euo pipefail
if [ "$#" -eq 0 ]; then echo "Usage: $0 command [arguments...]" >&2; exit 2; fi
test_temp_root=${PULSE_TEST_TEMP_ROOT:-/data/temp}
mkdir -p "$test_temp_root"
test_temp_root=$(realpath "$test_temp_root")
owned=$(mktemp -d "$test_temp_root/pulse-test-pg.XXXXXX")
pgbin=${PULSE_TEST_PG_BIN:-"$(mise where postgres)/bin"}
child=0
run_pg(){ if [ "$(id -u)" = 0 ]; then runuser -u postgres -- "$@"; else "$@"; fi; }
cleanup(){
 rc=$?; trap - EXIT; set +e
 if [ "$child" != 0 ]; then kill -TERM -- "-$child" 2>/dev/null; wait "$child" 2>/dev/null; fi
 if run_pg "$pgbin/pg_ctl" -D "$owned/pg" status >/dev/null 2>&1; then
  remaining=$("$pgbin/psql" -h "$owned/socket" -U postgres -d postgres -Atc "SELECT count(*) FROM pg_database WHERE datname NOT IN ('postgres','template0','template1')")
  if [ "$remaining" = 0 ]; then echo OWNED_DATABASE_CLEANUP_VERIFIED; else echo "Owned test databases remain at $owned" >&2; rc=1; fi
  if ! run_pg "$pgbin/pg_ctl" -D "$owned/pg" -m fast -w stop >"$owned/stop.log" 2>&1; then echo "Owned cluster cleanup blocked at $owned" >&2; exit 1; fi
 fi
 case "$owned" in "$test_temp_root"/pulse-test-pg.*) rm -rf -- "$owned";; *) exit 1;; esac
 test ! -e "$owned" || exit 1
 echo OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
expected=$(mise exec -- postgres --version)
test "$("$pgbin/postgres" --version)" = "$expected"
run_pg test -x "$pgbin/initdb"
mkdir -p "$owned/pg" "$owned/socket" "$owned/build"
if [ "$(id -u)" = 0 ]; then chmod 755 "$owned"; chown postgres:postgres "$owned/pg" "$owned/socket"; fi
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build"
export PULSE_TEST_DATABASE_URL="postgres://postgres@localhost/postgres?host=$owned/socket&port=5432&sslmode=disable"
export PULSE_UI_FIXTURE=0
run_pg "$pgbin/initdb" -D "$owned/pg" -U postgres --auth=trust --no-locale >"$owned/init.log" 2>&1 || { tail -n 15 "$owned/init.log"; exit 1; }
run_pg "$pgbin/pg_ctl" -D "$owned/pg" -o "-k $owned/socket -c listen_addresses='' -c max_connections=100" -w start >"$owned/start.log" 2>&1 || { tail -n 15 "$owned/start.log"; exit 1; }
setsid "$@" &
child=$!
set +e
wait "$child"
rc=$?
set -e
child=0
exit "$rc"
