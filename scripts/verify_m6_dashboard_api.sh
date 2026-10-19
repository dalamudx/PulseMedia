#!/usr/bin/env bash
# mise exec -- bash scripts/verify_m6_dashboard_api.sh [public]
# Real Cookie/App read projections only; no workers, transfers or physical sampler.
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# -gt 1 || ${1:-public} != public ]]; then
  echo 'Usage: verify_m6_dashboard_api.sh [public]' >&2
  exit 2
fi
: "${PULSE_POSTGRES_VERSION:?Run with mise exec}"
: "${PULSE_M4_POSTGRES_IMAGE:?Run with mise exec}"
[[ $PULSE_POSTGRES_VERSION == 18.6 ]] || { echo 'Exact mise PostgreSQL 18.6 required' >&2; exit 1; }
# Never pull an image or install a tool as a side effect of this acceptance.
podman image exists "$PULSE_M4_POSTGRES_IMAGE" || { echo 'Pinned mise PostgreSQL image must already be installed' >&2; exit 1; }
mkdir -p /data/temp
owned=$(mktemp -d /data/temp/pulse-m6-dashboard.XXXXXX)
validator=
cleanup() {
  rc=$?
  trap - EXIT INT TERM
  if [[ -n $validator ]]; then
    kill -TERM -- "-$validator" 2>/dev/null || true
    # timeout also bounds cancellation; wait joins the validator before deletion.
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
    /data/temp/pulse-m6-dashboard.*) test ! -L "$owned"; rm -rf -- "$owned" ;;
    *) exit 98 ;;
  esac
  test ! -e "$owned" || exit 1
  echo M6_DASHBOARD_VALIDATOR_AND_TEMP_CLEANUP_VERIFIED
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build"
export GOCACHE="$owned/go-cache" GOLANGCI_LINT_CACHE="$owned/lint-cache"
export PULSE_M6_DASHBOARD_ACCEPTANCE=1
# Keep execution offline; .mise.toml remains the exact toolchain source.
export GOPROXY=off GOTOOLCHAIN=local
mkdir "$GOTMPDIR" "$GOCACHE" "$GOLANGCI_LINT_CACHE"
# Select only the existing public auth initializer and this acceptance file.
setsid timeout --signal=TERM --kill-after=30s 600s mise exec -- \
  bash scripts/with_podman_test_postgres.sh \
  go test -race ./internal/httpapi/auth_test.go ./internal/httpapi/m6_dashboard_acceptance_test.go \
  -run '^TestM6Dashboard.*HTTPPG$' -count=1 -v -timeout=300s \
  >"$owned/acceptance.log" 2>&1 &
validator=$!
rc=0
wait "$validator" || rc=$?
if kill -0 -- "-$validator" 2>/dev/null; then
  echo 'Validator returned with remaining processes' >&2
  exit 1
fi
validator=
if [[ $rc != 0 ]]; then
  tail -n 100 "$owned/acceptance.log"
  exit "$rc"
fi
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then
  echo M6_DASHBOARD_UNEXPECTED_SKIP >&2
  exit 1
fi
for name in Library Capacity Activity AuthorizationAndFault; do
  if [[ $(grep -c "^--- PASS: TestM6Dashboard${name}HTTPPG (" "$owned/acceptance.log" || true) != 1 ]]; then
    tail -n 100 "$owned/acceptance.log"
    echo "M6_DASHBOARD_MISSING_TOP_LEVEL_PASS name=$name" >&2
    exit 1
  fi
done
actual=$(grep -c '^--- PASS: TestM6Dashboard' "$owned/acceptance.log" || true)
[[ $actual == 4 ]] || { echo "Unexpected top-level count: $actual" >&2; exit 1; }
grep -qx PASS "$owned/acceptance.log" || { echo 'Missing terminal Go PASS' >&2; exit 1; }
for receipt in OWNED_PODMAN_TEST_DATABASE_CLEANUP_VERIFIED OWNED_PODMAN_TEST_CONTAINER_AND_TEMP_CLEANUP_VERIFIED; do
  grep -qx "$receipt" "$owned/acceptance.log" || { echo "Missing cleanup receipt: $receipt" >&2; exit 1; }
done
grep -E '^--- PASS:|^ok[[:space:]]|^OWNED_PODMAN_|M6_DASHBOARD_AUTOTV_NOT_COVERED' "$owned/acceptance.log"
echo 'M6_DASHBOARD_API_ACCEPTANCE_EXECUTED tests=4 skips=0 realCookie=true AppReaders=true PG=18.6 race=true autoTV=not-covered ingestion=not-covered physicalSampler=not-covered'
