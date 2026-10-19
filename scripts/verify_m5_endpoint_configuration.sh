#!/usr/bin/env bash
# mise exec -- bash scripts/verify_m5_endpoint_configuration.sh [public]
# Public Cookie/PG/native acceptance only; Main owns browser acceptance.
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [ "$#" -gt 1 ] || [ "${1:-public}" != public ]; then
echo 'Usage: verify_m5_endpoint_configuration.sh [public]' >&2
exit 2
fi
if [ "${PULSE_M5_ENDPOINT_OWNED_PG:-}" != 1 ]; then
exec env PULSE_M5_ENDPOINT_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m5_endpoint_configuration.sh public
fi
: "${PULSE_TEST_DATABASE_URL:?private PostgreSQL fixture required}"
owned=$(mktemp -d /data/temp/pulse-m5-endpoint.XXXXXX)
cleanup() {
rc=$?
trap - EXIT
case "$owned" in /data/temp/pulse-m5-endpoint.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
test ! -e "$owned" || exit 1
echo M5_ENDPOINT_OWNED_TEMP_CLEANUP_VERIFIED
exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build"
export GOCACHE="$owned/go-cache"
export PULSE_M5_ENDPOINT_CONFIGURATION_ACCEPTANCE=1
mkdir -p "$GOTMPDIR" "$GOCACHE"
# Explicit file set avoids unrelated acceptance opt-ins and includes the existing
# real Cookie/PG initializer. No generated code or frontend output is written.
if ! mise exec -- go test -race ./internal/httpapi/auth_test.go ./internal/httpapi/m5_endpoint_configuration_acceptance_test.go -run '^TestM5EndpointConfiguration.*HTTPPG$' -count=1 -v -timeout=300s >"$owned/acceptance.log" 2>&1; then
tail -n 100 "$owned/acceptance.log"
exit 1
fi
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M5_ENDPOINT_UNEXPECTED_SKIP >&2; exit 1; fi
actual=$(grep -c '^--- PASS: TestM5EndpointConfiguration' "$owned/acceptance.log")
if [ "$actual" != 8 ]; then echo "M5_ENDPOINT_MISSING_EXECUTION expected=8 actual=$actual" >&2; exit 1; fi
subcases=$(grep -c '^[[:space:]]*--- PASS: TestM5EndpointConfiguration.*/' "$owned/acceptance.log" || true)
grep -E '^--- PASS:|^ok[[:space:]]' "$owned/acceptance.log"
echo "M5_ENDPOINT_ACCEPTANCE_EXECUTED mode=public tests=$actual subcases=$subcases realCookie=true PG=18.6 native=qbittorrent,transmission,jellyfin,emby race=true"