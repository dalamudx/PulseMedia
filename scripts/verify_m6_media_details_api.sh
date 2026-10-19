#!/usr/bin/env bash
# mise exec -- bash scripts/verify_m6_media_details_api.sh
# Real Cookie/App/PG/native TMDB acceptance; no workers or public network.
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
[[ $# == 0 ]] || { echo 'Usage: verify_m6_media_details_api.sh' >&2; exit 2; }
: "${PULSE_POSTGRES_VERSION:?Run with mise exec}"
: "${PULSE_M4_POSTGRES_IMAGE:?Run with mise exec}"
[[ $PULSE_POSTGRES_VERSION == 18.6 ]] || { echo 'Exact mise PostgreSQL 18.6 required' >&2; exit 1; }
podman image exists "$PULSE_M4_POSTGRES_IMAGE" || { echo 'Pinned mise PostgreSQL image must already be installed' >&2; exit 1; }
mkdir -p /data/temp
owned=$(mktemp -d /data/temp/pulse-m6-media-details.XXXXXX)
validator=
cleanup() {
  rc=$?
  trap - EXIT INT TERM
  if [[ -n $validator ]]; then
    kill -TERM -- "-$validator" 2>/dev/null || true
    wait "$validator" 2>/dev/null || true
    for attempt in {1..30}; do
      if ! kill -0 -- "-$validator" 2>/dev/null; then break; fi
      sleep 1
    done
    if kill -0 -- "-$validator" 2>/dev/null; then
      kill -KILL -- "-$validator" 2>/dev/null || true
      echo 'Validator process group required forced termination' >&2
      rc=1
    fi
  fi
  case "$owned" in
    /data/temp/pulse-m6-media-details.*) test ! -L "$owned"; rm -rf -- "$owned" ;;
    *) exit 98 ;;
  esac
  test ! -e "$owned" || exit 1
  echo M6_METADATA_DETAILS_VALIDATOR_CACHE_AND_TEMP_CLEANUP_VERIFIED
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build"
export GOCACHE="$owned/go-cache" GOLANGCI_LINT_CACHE="$owned/lint-cache"
export GOPROXY=off GOTOOLCHAIN=local
mkdir "$GOTMPDIR" "$GOCACHE" "$GOLANGCI_LINT_CACHE"
run_owned() {
  local deadline=$1 log=$2 result=0
  shift 2
  setsid timeout --signal=TERM --kill-after=30s "$deadline" "$@" >"$log" 2>&1 &
  validator=$!
  wait "$validator" || result=$?
  if kill -0 -- "-$validator" 2>/dev/null; then
    echo 'Validator returned with remaining processes' >&2
    return 1
  fi
  validator=
  return "$result"
}
test_rc=0
run_owned 900s "$owned/acceptance.log" mise exec -- \
  bash scripts/with_podman_test_postgres.sh \
  go test -race ./internal/app -run '^TestM6MetadataDetail(Display|Faults|Authorization|Unavailable)NativeHTTPPG$' \
  -count=1 -v -timeout=300s || test_rc=$?
lint_rc=0
run_owned 300s "$owned/lint.log" mise exec -- \
  golangci-lint run ./internal/app --timeout=4m || lint_rc=$?
cat "$owned/lint.log"
if [[ $lint_rc == 0 ]]; then echo M6_METADATA_DETAILS_SCOPED_LINT_PASS; fi
if [[ $test_rc != 0 ]]; then
  grep -E -- '^--- (PASS|FAIL):|^[[:space:]]+--- FAIL:|\.go:[0-9]+:|^FAIL|^#|^OWNED_PODMAN_' "$owned/acceptance.log" || tail -n 100 "$owned/acceptance.log"
  exit "$test_rc"
fi
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then
  echo M6_METADATA_DETAILS_UNEXPECTED_SKIP >&2
  exit 1
fi
for name in Display Faults Authorization Unavailable; do
  if [[ $(grep -c "^--- PASS: TestM6MetadataDetail${name}NativeHTTPPG (" "$owned/acceptance.log" || true) != 1 ]]; then
    tail -n 100 "$owned/acceptance.log"
    echo "M6_METADATA_DETAILS_MISSING_TOP_LEVEL_PASS name=$name" >&2
    exit 1
  fi
done
actual=$(grep -c '^--- PASS: TestM6MetadataDetail' "$owned/acceptance.log" || true)
[[ $actual == 4 ]] || { echo "Unexpected top-level PASS count: $actual" >&2; exit 1; }
grep -qx PASS "$owned/acceptance.log" || { echo 'Missing terminal Go PASS' >&2; exit 1; }
for receipt in OWNED_PODMAN_TEST_DATABASE_CLEANUP_VERIFIED OWNED_PODMAN_TEST_CONTAINER_AND_TEMP_CLEANUP_VERIFIED; do
  grep -qx "$receipt" "$owned/acceptance.log" || { echo "Missing cleanup receipt: $receipt" >&2; exit 1; }
done
grep -E '^--- PASS:|^[[:space:]]+--- PASS:|^ok[[:space:]]|^OWNED_PODMAN_' "$owned/acceptance.log"
[[ $lint_rc == 0 ]] || exit "$lint_rc"
subtests=$(grep -c '^[[:space:]]\+--- PASS:' "$owned/acceptance.log" || true)
echo "M6_METADATA_DETAILS_API_ACCEPTANCE_EXECUTED tests=$actual subtests=$subtests skips=0 realCookie=true realTokenScope=true App=true PG=18.6 nativeTMDB=true race=true businessWrites=0"
echo 'M6_METADATA_DETAILS_BOUNDARY App.Run/workers/browser/subscription/acquisition/public-TMDB/post-network-DB-permission-gate=not-covered'
