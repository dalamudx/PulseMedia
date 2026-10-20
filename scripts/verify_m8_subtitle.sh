#!/usr/bin/env bash
# Main-owned acceptance: mise exec -- bash scripts/verify_m8_subtitle.sh
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m8_subtitle.sh' >&2; exit 2; fi
if [[ ${PULSE_M8_SUBTITLE_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M8_SUBTITLE_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m8_subtitle.sh
fi
: "${PULSE_TEST_DATABASE_URL:?private PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m8-subtitle.XXXXXX)
cleanup() {
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m8-subtitle.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M8_SUBTITLE_OWNED_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
module_cache=$(mise exec -- go env GOMODCACHE)
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache" GOMODCACHE="$owned/modules"
export GOPROXY="file://$module_cache/cache/download"
export XDG_CACHE_HOME="$owned/cache" XDG_CONFIG_HOME="$owned/config"
mkdir -p "$GOTMPDIR" "$GOCACHE" "$GOMODCACHE"
mise exec -- go test -race ./internal/app -run '^TestM8SubtitleRuntimeAcceptancePG$' -count=1 -v -timeout=480s 2>&1 | tee "$owned/acceptance.log"
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M8_SUBTITLE_UNEXPECTED_SKIP >&2; exit 1; fi
test "$(grep -c '^--- PASS: TestM8SubtitleRuntimeAcceptancePG (' "$owned/acceptance.log")" = 1
test "$(grep -c -- '--- PASS: TestM8SubtitleRuntimeAcceptancePG/SQL_PREPARED/[^/]*/[^/]* (' "$owned/acceptance.log")" = 10
test "$(grep -c 'M8_SUBTITLE_CASE_PASS ' "$owned/acceptance.log")" = 10
test "$(grep -c 'M8_SUBTITLE_RUNTIME_PASS ' "$owned/acceptance.log")" = 1
echo M8_SUBTITLE_ACCEPTANCE_EXECUTED_top_tests=1_scenarios=10_skipped=0_inventory=SQL_PREPARED_network=OWNED_HTTP_live_shooter=false_player=exact_basename_only
