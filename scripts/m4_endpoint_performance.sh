#!/usr/bin/env bash
# Independent active synthetic loopback evidence, excluding the scale baseline.
# Usage: mise exec -- bash scripts/m4_endpoint_performance.sh
set -euo pipefail
umask 077
if [[ $# != 0 ]]; then printf 'Usage: m4_endpoint_performance.sh\n' >&2; exit 2; fi
repo="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
mkdir -p /data/temp
endpoint_work="$(mktemp -d /data/temp/pulse-m4-endpoint.XXXXXX)"
cleanup() {
  result=$?
  trap - EXIT
  case "$endpoint_work" in /data/temp/pulse-m4-endpoint.*) test ! -L "$endpoint_work"; rm -rf -- "$endpoint_work" ;; *) exit 98 ;; esac
  test ! -e "$endpoint_work" || exit 1
  printf 'M4_ENDPOINT_TEMP_CLEANUP_VERIFIED\n'
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$endpoint_work" TMP="$endpoint_work" TEMP="$endpoint_work"
export GOCACHE="$endpoint_work/go-cache" GOFLAGS=""
export PULSE_M4_ENDPOINT_PERFORMANCE=1
cd "$repo"
# No race instrumentation, no disabled compiler optimization; App.Run itself
# serves real loopback HTTP in the test process with all normal workers enabled.
bash scripts/with_podman_test_postgres.sh bash -c '
  pg_work=${TMPDIR%/build}
  pg_container="pulse-pg-test-${pg_work##*.}"
  podman inspect --format "M4_ENDPOINT_PG memory_limit_bytes={{.HostConfig.Memory}} cpu_quota={{.HostConfig.CpuQuota}} cpu_period={{.HostConfig.CpuPeriod}} cpuset_cpus={{.HostConfig.CpusetCpus}} network={{.HostConfig.NetworkMode}}" "$pg_container"
  exec go test ./internal/app -run "^TestM4EndpointPerformance$" -count=1 -v -timeout=4m
'
