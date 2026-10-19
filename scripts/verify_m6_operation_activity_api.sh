#!/usr/bin/env bash
# mise exec -- bash scripts/verify_m6_operation_activity_api.sh [public]
# Public Cookie/App/PG reads; only the explicit native fixture observation writes.
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
[[ $# == 0 || ( $# == 1 && $1 == public ) ]] || { echo 'Usage: verify_m6_operation_activity_api.sh [public]' >&2; exit 2; }
: "${PULSE_POSTGRES_VERSION:?Run with mise exec}"
: "${PULSE_M4_POSTGRES_IMAGE:?Run with mise exec}"
[[ $PULSE_POSTGRES_VERSION == 18.6 && $PULSE_M4_POSTGRES_IMAGE == *@sha256:* ]] || { echo 'Pinned mise PostgreSQL 18.6 required' >&2; exit 1; }
podman image exists "$PULSE_M4_POSTGRES_IMAGE" || { echo 'Pinned mise image must already be installed' >&2; exit 1; }
mkdir -p /data/temp
owned=$(mktemp -d /data/temp/pulse-m6-operation-activity.XXXXXX)
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
    /data/temp/pulse-m6-operation-activity.*) test ! -L "$owned"; rm -rf -- "$owned" ;;
    *) exit 98 ;;
  esac
  test ! -e "$owned" || exit 1
  echo M6_OPERATION_ACTIVITY_VALIDATOR_CACHE_AND_TEMP_CLEANUP_VERIFIED
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
run_owned 600s "$owned/acceptance.log" mise exec -- \
  bash scripts/with_podman_test_postgres.sh \
  go test -v -race -count=1 -timeout=300s -run '^TestM6Operation.*HTTPPG$' ./internal/app || test_rc=$?
if [[ $test_rc != 0 ]]; then
  grep -E -- '^--- (PASS|FAIL):|^[[:space:]]+--- FAIL:|\.go:[0-9]+:|^FAIL|^#|^OWNED_PODMAN_' "$owned/acceptance.log" || tail -n 100 "$owned/acceptance.log"
  exit "$test_rc"
fi
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then
  echo M6_OPERATION_ACTIVITY_UNEXPECTED_SKIP >&2
  exit 1
fi
for name in NativeTelemetry Timeline Authorization StorageFault; do
  if [[ $(grep -c "^--- PASS: TestM6Operation${name}HTTPPG (" "$owned/acceptance.log" || true) != 1 ]]; then
    tail -n 100 "$owned/acceptance.log"
    echo "M6_OPERATION_ACTIVITY_MISSING_TOP_LEVEL_PASS name=$name" >&2
    exit 1
  fi
done
actual=$(grep -c '^--- PASS: TestM6Operation' "$owned/acceptance.log" || true)
[[ $actual == 4 ]] || { echo "Unexpected top-level PASS count: $actual" >&2; exit 1; }
grep -qx PASS "$owned/acceptance.log" || { echo 'Missing terminal Go PASS' >&2; exit 1; }
for receipt in OWNED_PODMAN_TEST_DATABASE_CLEANUP_VERIFIED OWNED_PODMAN_TEST_CONTAINER_AND_TEMP_CLEANUP_VERIFIED; do
  grep -qx "$receipt" "$owned/acceptance.log" || { echo "Missing cleanup receipt: $receipt" >&2; exit 1; }
done
grep -E '^--- PASS:|^[[:space:]]+--- PASS:|^ok[[:space:]]|^OWNED_PODMAN_|M6_OPERATION_AUTOTV_NOT_COVERED' "$owned/acceptance.log"
lint_rc=0
run_owned 120s "$owned/lint.log" mise exec -- \
  golangci-lint run ./internal/app --timeout=110s || lint_rc=$?
cat "$owned/lint.log"
[[ $lint_rc == 0 ]] || exit "$lint_rc"
echo M6_OPERATION_ACTIVITY_SCOPED_LINT_PASS
subtests=$(grep -c '^[[:space:]]\+--- PASS:' "$owned/acceptance.log" || true)
echo "M6_OPERATION_ACTIVITY_API_ACCEPTANCE_EXECUTED tests=$actual subtests=$subtests skips=0 realCookie=true realTokenScope=true App=true PG=18.6 schema=93 nativeQB=true race=true GETbusinessWrites=0 GETnativeCalls=0"
echo 'M6_OPERATION_ACTIVITY_BOUNDARY acquisition/Add/real-service-file-completion/autoTV/completeTV/upgrade/App.Run/workers/browser=not-covered'
