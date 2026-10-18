#!/usr/bin/env bash
# Opt-in synthetic baseline; private PG18 only. All scratch is removed on exit.
# Usage: mise exec -- bash scripts/m4_performance.sh [samples-per-route=100] [concurrency=4]
set -euo pipefail
umask 077
samples="${1:-100}"
concurrency="${2:-4}"
if [[ $# -gt 2 || ! "$samples" =~ ^[1-9][0-9]{0,4}$ || ! "$concurrency" =~ ^[1-9][0-9]?$ ]] || ((samples < 20 || samples > 10000 || concurrency < 1 || concurrency > 32)); then
  printf 'Usage: m4_performance.sh [samples 20..10000] [concurrency 1..32]\n' >&2
  exit 2
fi
repo="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
mkdir -p /data/temp
perf_work="$(mktemp -d /data/temp/pulse-m4-performance.XXXXXX)"
cleanup() {
  result=$?
  trap - EXIT
  case "$perf_work" in /data/temp/pulse-m4-performance.*) test ! -L "$perf_work"; rm -rf -- "$perf_work" ;; *) exit 98 ;; esac
  test ! -e "$perf_work" || exit 1
  printf 'M4_PERFORMANCE_TEMP_CLEANUP_VERIFIED\n'
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$perf_work" TMP="$perf_work" TEMP="$perf_work"
export GOCACHE="$perf_work/go-cache" GOFLAGS=""
export PULSE_M4_PERFORMANCE=1 PULSE_M4_PERFORMANCE_SAMPLES="$samples" PULSE_M4_PERFORMANCE_CONCURRENCY="$concurrency"
export PULSE_M4_PERFORMANCE_BINARY="$perf_work/pulsemedia"
cd "$repo"
# Production compiler defaults: optimization/inlining enabled, no race instrumentation.
CGO_ENABLED=0 go build -o "$PULSE_M4_PERFORMANCE_BINARY" ./cmd/pulsemedia
printf 'M4_PRODUCTION_BUILD optimized=true race=false CGO_ENABLED=0\n'
bash scripts/with_podman_test_postgres.sh bash -c '
  pg_work=${TMPDIR%/build}
  pg_container="pulse-pg-test-${pg_work##*.}"
  podman inspect --format "M4_PG_CONTAINER memory_limit_bytes={{.HostConfig.Memory}} cpu_quota={{.HostConfig.CpuQuota}} cpu_period={{.HostConfig.CpuPeriod}} cpu_shares={{.HostConfig.CpuShares}} cpuset_cpus={{.HostConfig.CpusetCpus}} network={{.HostConfig.NetworkMode}}" "$pg_container"
  exec go test ./internal/app -run "^TestM4PerformanceBaseline$" -count=1 -v -timeout=25m
'
