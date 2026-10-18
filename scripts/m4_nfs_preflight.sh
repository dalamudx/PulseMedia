#!/usr/bin/env bash
# Checks mount-policy prerequisites only; never claims NFS fault acceptance.
set -euo pipefail
umask 077
if [[ -n "${CONTAINER_HOST:-}" || -n "${CONTAINER_CONNECTION:-}" ]]; then
  printf 'Direct local Podman is required; remote environment is set.\n' >&2
  exit 2
fi
image="${PULSE_M4_NFS_PROBE_IMAGE:?Set the immutable probe image through .mise.toml}"
if [[ ! "$image" =~ @sha256:[a-f0-9]{64}$ ]]; then
  printf 'PULSE_M4_NFS_PROBE_IMAGE must use an immutable OCI digest.\n' >&2
  exit 2
fi
mkdir -p /data/temp
scratch="$(mktemp -d /data/temp/pulse-m4-nfs-preflight.XXXXXX)"
export TMPDIR="$scratch" TMP="$scratch" TEMP="$scratch"
cleanup() {
  result=$?
  trap - EXIT
  if [[ -s "$scratch/client.cid" ]]; then
    cid="$(<"$scratch/client.cid")"
    if [[ ! "$cid" =~ ^[a-f0-9]{64}$ ]] || [[ "$(podman inspect --format '{{index .Config.Labels "io.pulsemedia.nfs-owner"}}' "$cid")" != "$scratch" ]]; then
      printf 'Cleanup ownership check failed; retained %s\n' "$scratch" >&2
      exit 98
    fi
    if ! podman rm --force --time 5 "$cid" >/dev/null || podman container exists "$cid"; then
      printf 'Container cleanup failed; retained %s\n' "$scratch" >&2
      exit 98
    fi
  fi
  case "$scratch" in /data/temp/pulse-m4-nfs-preflight.*) rm -rf -- "$scratch" ;; *) exit 98 ;; esac
  test ! -e "$scratch" || exit 98
  printf 'OWNED_NFS_PREFLIGHT_CLEANUP_VERIFIED\n'
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if [[ "$(podman info --format '{{.Host.Security.Rootless}}')" != false ]]; then
  printf 'BLOCKED: this kernel NFS slice requires rootful isolated containers.\n' >&2
  exit 77
fi
podman image inspect "$image" --format 'PROBE_IMAGE={{.Id}} {{json .RepoDigests}}'
# No host paths, devices, network, propagation, policy relaxation, or nfsd.
# /data/temp below is entirely inside this container's private root filesystem.
podman run --pull never --timeout 20 --stop-timeout 5 \
  --name "pulse-m4-nfs-preflight-${scratch##*.}" --cidfile "$scratch/client.cid" \
  --label "io.pulsemedia.nfs-owner=$scratch" \
  --network none --ipc private --pid private \
  --cap-add SYS_ADMIN --security-opt no-new-privileges \
  "$image" sh -eu -c '
    IFS= read -r release < /etc/alpine-release
    printf "PROBE_ALPINE=%s\n" "$release"
    if test -r /proc/self/attr/current; then
      IFS= read -r profile < /proc/self/attr/current
      printf "LSM=%s\n" "$profile"
    fi
    while IFS= read -r line; do
      case "$line" in CapEff:*|Seccomp:*) printf "%s\n" "$line" ;; esac
    done < /proc/self/status
    mkdir -p /data/temp/mount-probe
    if ! mount -t tmpfs -o size=1m,mode=0700 tmpfs /data/temp/mount-probe; then
      printf "BLOCKED: private mount denied; do not relax host/container policy automatically.\n" >&2
      exit 77
    fi
    umount /data/temp/mount-probe
    nfs_available=false
    while read -r first second; do
      case "$first $second" in "nfs "|"nfs4 "|"nodev nfs"|"nodev nfs4") nfs_available=true ;; esac
    done < /proc/filesystems
    if test "$nfs_available" != true; then
      printf "BLOCKED: kernel NFS client is not registered; no host module changes attempted.\n" >&2
      exit 77
    fi
    printf "PRIVATE_MOUNT_PREREQUISITES_OK; real NFS mount and production fault checks still required.\n"
  '
