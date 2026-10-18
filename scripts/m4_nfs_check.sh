#!/usr/bin/env bash
# Run inside an isolated NFS client container, after mounting its owned export.
set -euo pipefail
umask 077
: "${PULSE_M4_NFS_ROOT:?Set the owned NFS root inside the client namespace}"
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
mkdir -p /data/temp
scratch="$(mktemp -d /data/temp/pulse-m4-nfs-check.XXXXXX)"
cleanup() {
  result=$?
  trap - EXIT
  case "$scratch" in /data/temp/pulse-m4-nfs-check.*) rm -rf -- "$scratch" ;; *) exit 98 ;; esac
  test ! -e "$scratch" || exit 98
  printf 'OWNED_NFS_CHECK_TEMP_CLEANUP_VERIFIED\n'
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$scratch" TMP="$scratch" TEMP="$scratch"
cd "$repo_root"
mise exec -- go test -race ./internal/platform/rootfs \
  -run '^TestM4NFSProductionRootfsSmoke$' -count=1 -timeout=45s -v
