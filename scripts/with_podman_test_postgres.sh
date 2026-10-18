#!/usr/bin/env bash
# Explicit private PG18 container: no TCP, no shared databases or credentials.
set -euo pipefail
umask 077
: "${PULSE_M4_POSTGRES_IMAGE:?Run this command with mise exec}"
if [[ $# == 0 ]]; then printf 'Usage: with_podman_test_postgres.sh COMMAND [ARG...]\n' >&2; exit 2; fi
mkdir -p /data/temp
pg_work="$(mktemp -d /data/temp/pulse-podman-test-pg.XXXXXX)"
pg_container="pulse-pg-test-${pg_work##*.}"
cleanup() {
  result=$?
  trap - EXIT
  if podman container exists "$pg_container"; then
    if [[ "$(podman inspect --format '{{index .Config.Labels "io.pulsemedia.test-state"}}' "$pg_container")" != "$pg_work" ]]; then
      printf 'PG cleanup ownership mismatch; retained %s\n' "$pg_work" >&2
      exit 98
    fi
    if [[ "$result" != 0 ]]; then podman logs --tail 25 "$pg_container" >&2 || true; fi
    remaining="$(podman exec "$pg_container" psql -h /var/run/postgresql -U postgres -d postgres -Atqc "SELECT count(*) FROM pg_database WHERE datname NOT IN ('postgres','template0','template1')" 2>/dev/null)" || remaining=unavailable
    if [[ "$remaining" == 0 ]]; then
      printf 'OWNED_PODMAN_TEST_DATABASE_CLEANUP_VERIFIED\n'
    else
      printf 'Owned test databases were not confirmed clean: %s\n' "$pg_work" >&2
      result=1
    fi
    if ! podman rm -f --volumes --time 15 "$pg_container" >/dev/null; then
      printf 'Owned PG container cleanup failed: %s; retained %s\n' "$pg_container" "$pg_work" >&2
      exit 1
    fi
  fi
  case "$pg_work" in /data/temp/pulse-podman-test-pg.*) test ! -L "$pg_work"; rm -rf -- "$pg_work" ;; *) exit 98 ;; esac
  test ! -e "$pg_work" || exit 1
  printf 'OWNED_PODMAN_TEST_CONTAINER_AND_TEMP_CLEANUP_VERIFIED\n'
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir "$pg_work/socket" "$pg_work/build" "$pg_work/data"
chmod 1777 "$pg_work/socket"
chmod 0755 "$pg_work/data"
export TMPDIR="$pg_work/build" TMP="$pg_work/build" TEMP="$pg_work/build"
if [[ -z "${PULSE_TEST_PG_BIN:-}" ]]; then
  PULSE_TEST_PG_BIN="$(dirname -- "$(command -v pg_dump)")"
  export PULSE_TEST_PG_BIN
fi
podman run -d --platform "linux/$(go env GOARCH)" --name "$pg_container" --network none \
  --label "io.pulsemedia.test-state=$pg_work" \
  --env POSTGRES_HOST_AUTH_METHOD=trust \
  --env TMPDIR=/var/run/postgresql \
  --mount "type=bind,source=$pg_work/socket,destination=/var/run/postgresql" \
  --mount "type=bind,source=$pg_work/data,destination=/var/lib/postgresql" \
  "$PULSE_M4_POSTGRES_IMAGE" postgres \
  -c listen_addresses= -c unix_socket_permissions=0700 >/dev/null
ready=0
for attempt in {1..45}; do
  if podman exec "$pg_container" sh -c 'test "$(cat /proc/1/comm)" = postgres && exec psql -h /var/run/postgresql -U postgres -d postgres -Atqc "SHOW server_version_num"' >/dev/null 2>&1; then ready=1; break; fi
  if [[ "$(podman inspect --format '{{.State.Running}}' "$pg_container")" != true ]]; then break; fi
  sleep 1
done
if [[ "$ready" != 1 ]]; then printf 'Owned PostgreSQL initialization failed\n' >&2; exit 1; fi
export PULSE_TEST_DATABASE_URL="postgres://postgres@localhost/postgres?host=$pg_work/socket&sslmode=disable"
"$@"
