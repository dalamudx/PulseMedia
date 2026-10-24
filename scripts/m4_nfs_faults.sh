#!/usr/bin/env bash
# Genuine kernel NFS client, task-owned userland server; invoke via mise exec.
set -Eeuo pipefail
umask 077
repo="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
if [[ ${1:-} == --client ]]; then
  : "${PULSE_M4_NFS_WORK:?}" "${PULSE_M4_NFS_PORT:?}"
  : "${PULSE_M4_NFS_PARENT_MNT:?}"
  # Podman/netavark lifecycle belongs to the original host mount namespace.
  # Native file I/O stays here in the independent NFS client namespace.
  podman() { command nsenter --mount="$PULSE_M4_NFS_PARENT_MNT" -- podman "$@"; }
  export -f podman
  mounted=0
  client_cleanup() {
    local rc=$?
    trap - EXIT
    podman unpause "$PULSE_M4_NFS_CID" >/dev/null 2>&1 || true
    podman start "$PULSE_M4_NFS_CID" >/dev/null
    if [[ $mounted == 1 ]]; then
      if ! umount "$PULSE_M4_NFS_ROOT"; then
        printf 'NFS_UNMOUNT_FAILED retained=%s cid=%s\n' "$PULSE_M4_NFS_WORK" "$PULSE_M4_NFS_CID" >&2
        touch "$PULSE_M4_NFS_WORK/retain"
        exit 98
      fi
    fi
    printf 'PRIVATE_NFS_UNMOUNT_VERIFIED\n'
    exit "$rc"
  }
  trap client_cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  export PULSE_M4_NFS_OPTIONS="retry=0,sec=sys,vers=4.1,proto=tcp,port=$PULSE_M4_NFS_PORT,hard,timeo=10,retrans=2,actimeo=0,lookupcache=none"
  timeout 10 mount -v -t nfs4 -o "$PULSE_M4_NFS_OPTIONS" "$PULSE_M4_NFS_ADDRESS:/pulse" "$PULSE_M4_NFS_ROOT"
  mounted=1
  findmnt -rn -M "$PULSE_M4_NFS_ROOT" -o TARGET,SOURCE,FSTYPE,OPTIONS
  test "$(stat -f -c %T "$PULSE_M4_NFS_ROOT")" = nfs
  printf 'ACTUAL_KERNEL_NFS41_MOUNT_VERIFIED namespace=%s\n' "$(readlink /proc/self/ns/mnt)"
  if [[ ${PULSE_M4_NFS_MOUNT_ONLY:-0} == 1 ]]; then exit 0; fi
  export PULSE_M4_NFS_FAULTS=1
  cd "$repo"
  if [[ ${PULSE_M8_NFS_WATCH_ACCEPTANCE:-0} == 1 ]]; then
    export PULSE_M8_UNTRACKED_ORGANIZATION_ACCEPTANCE=1
    nas_log="$PULSE_M4_NFS_WORK/m8-nfs-watch.log"
    if ! bash scripts/with_podman_test_postgres.sh go test -race ./internal/app -run '^TestM8NFS(RemoteWritePeriodicOrganization|OrganizationBlockedCancellation)$' -count=1 -timeout=12m -v >"$nas_log" 2>&1; then
      cat "$nas_log"
      exit 1
    fi
    cat "$nas_log"
    if command grep -q -- '--- SKIP:' "$nas_log"; then exit 1; fi
    test "$(command grep -c '^--- PASS: TestM8NFSRemoteWritePeriodicOrganization ' "$nas_log")" = 1
    test "$(command grep -c 'M8_NFS_PERIODIC_ORGANIZATION_PASS action=hardlink ' "$nas_log")" = 1
    for action in copy move; do
      test "$(command grep -Ec "M8_NFS_(PERIODIC_ORGANIZATION|UNSUPPORTED_PROTECTION)_PASS action=$action " "$nas_log")" = 1
    done
    test "$(command grep -c 'M8_NFS_ROLLBACK_RECOVERY_PASS action=hardlink ' "$nas_log")" = 1
    test "$(command grep -c '^--- PASS: TestM8NFSOrganizationBlockedCancellation ' "$nas_log")" = 1
    test "$(command grep -c 'M8_NFS_BLOCKED_ORGANIZATION_PASS ' "$nas_log")" = 1
    printf 'M8_NFS_WATCH_PERIODIC_ACCEPTANCE_PASS parents=2 scenarios=5 PG=18.6 mount=NFS41 watcher=UNAVAILABLE fallback=REAL_PERIODIC remote_write=SERVER_EXPORT hardlink=IMPORTED hardlink_tail_rollback=TOKEN2_OBSERVATION same_target_inode=true copy_move=CAPABILITY_CHECKED unsupported_noreplace=SOURCE_PRESERVED_NO_IMPORT original_receipts=true downloader_calls=0 immediate_latency=NOT_CLAIMED blocked_organization=CANCEL_WAITS_ACTUAL_IO retry_same_operation=TOKEN2 milestone=NOT_RELEASED\n'
  else
    bash scripts/with_podman_test_postgres.sh go test -race ./internal/app -run '^TestM4KernelNFSFaults$' -count=1 -timeout=8m -v
  fi
  exit
fi
: "${PULSE_M4_POSTGRES_IMAGE:?Use mise exec}" "${PULSE_M4_GANESHA_VERSION:?Use mise exec}"
work="$(mktemp -d /data/temp/pulse-m4-nfs.XXXXXX)"
export PULSE_M4_NFS_WORK="$work" TMPDIR="$work" TMP="$work" TEMP="$work"
export PULSE_M4_NFS_ROOT="$work/client"
suffix="${work##*.}"
name="pulse-m4-nfs-${suffix,,}"
network="$name"
image="localhost/$name:acceptance"
cid=''
cleanup() {
  local rc=$?
  trap - EXIT
  if [[ -n $cid ]] && podman container exists "$cid"; then
    test "$(podman inspect -f '{{index .Config.Labels "io.pulsemedia.nfs-owner"}}' "$cid")" = "$work" || exit 98
    podman unpause "$cid" >/dev/null 2>&1 || true
    podman start "$cid" >/dev/null 2>&1 || true
  fi
  if [[ -e $work/retain ]] || findmnt -rn -M "$PULSE_M4_NFS_ROOT" >/dev/null; then
    printf 'RETAINED_NFS_RESOURCES work=%s cid=%s network=%s image=%s\n' "$work" "$cid" "$network" "$image" >&2
    exit 98
  fi
  if [[ -n $cid ]]; then
    if [[ $rc != 0 ]]; then podman logs --tail 180 "$cid" >&2 || true; fi
    if ! podman rm -f --volumes --time 15 "$cid" >/dev/null; then
      if podman container exists "$cid"; then
        printf 'OWNED_NFS_CONTAINER_CLEANUP_BLOCKED work=%s cid=%s\n' "$work" "$cid" >&2
        exit 98
      fi
    fi
  fi
  if podman network exists "$network"; then
    test "$(podman network inspect -f '{{index .Labels "io.pulsemedia.nfs-owner"}}' "$network")" = "$work" || exit 98
    podman network rm "$network" >/dev/null
  fi
  if podman image exists "$image"; then
    test "$(podman image inspect -f '{{index .Labels "io.pulsemedia.nfs-owner"}}' "$image")" = "$work" || exit 98
    podman rmi "$image" >/dev/null
  fi
  case "$work" in /data/temp/pulse-m4-nfs.*) test ! -L "$work"; rm -rf -- "$work" ;; *) exit 98 ;; esac
  test ! -e "$work"
  ! podman container exists "$name"
  ! podman network exists "$network"
  ! podman image exists "$image"
  printf 'PARENT_MOUNT_ABSENT_OWNED_NFS_CONTAINER_NETWORK_IMAGE_TEMP_CLEANUP_VERIFIED\n'
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir "$work/client" "$work/export" "$work/build"
printf 'synthetic kernel NFS acceptance bytes\n' > "$work/export/Observed.mkv"
cat > "$work/build/Containerfile" <<EOF
FROM $PULSE_M4_POSTGRES_IMAGE
LABEL io.pulsemedia.nfs-owner="$work"
RUN apt-get update && apt-get install -y --no-install-recommends nfs-ganesha=$PULSE_M4_GANESHA_VERSION nfs-ganesha-vfs=$PULSE_M4_GANESHA_VERSION && rm -rf /var/lib/apt/lists/*
ENTRYPOINT ["/bin/sh", "-ec", "mkdir -p /run/rpcbind; /usr/sbin/rpcbind; exec /usr/bin/ganesha.nfsd -F -p /ganesha.pid -L /dev/stdout -f /etc/ganesha/ganesha.conf"]
EOF
cat > "$work/ganesha.conf" <<'EOF'
NFS_CORE_PARAM { Allow_Set_Io_Flusher_Fail = true; Protocols = 4; NFS_Port = 2049; Enable_NLM = false; Enable_RQUOTA = false; }
NFSv4 { Minor_Versions = 1; Graceless = true; Only_Numeric_Owners = true; }
EXPORT { Export_Id = 101; Path = /export; Pseudo = /pulse; Protocols = 4; Transports = TCP; Access_Type = RW; Squash = No_Root_Squash; SecType = sys; FSAL { Name = VFS; } }
LOG { Default_Log_Level = EVENT; }
EOF
platform="linux/$(go env GOARCH)"
podman build --platform "$platform" --layers=false --force-rm --label "io.pulsemedia.nfs-owner=$work" -t "$image" "$work/build"
podman network create --internal --label "io.pulsemedia.nfs-owner=$work" "$network" >/dev/null
nfs_gateway="$(podman network inspect -f '{{(index .Subnets 0).Gateway}}' "$network")"
[[ $nfs_gateway =~ ^[0-9]+\.[0-9]+\.[0-9]+\.1$ ]]
nfs_static_ip="${nfs_gateway%.*}.2"
cid="$(podman run -d --image-volume=ignore --platform "$platform" --stop-signal SIGTERM --name "$name" --label "io.pulsemedia.nfs-owner=$work" --network "$network" --ip "$nfs_static_ip" --cap-add DAC_READ_SEARCH --mount "type=bind,src=$work/export,dst=/export" --mount "type=bind,src=$work/ganesha.conf,dst=/etc/ganesha/ganesha.conf,ro" "$image")"
export PULSE_M4_NFS_CID="$cid"
# The host reaches its task-owned bridge directly; published-port NAT is not NFS evidence.
export PULSE_M4_NFS_ADDRESS="$(podman inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$cid")"
export PULSE_M4_NFS_PORT=2049
[[ $PULSE_M4_NFS_ADDRESS =~ ^[0-9.]+$ ]]
# Server readiness: bounded TCP probe before the hard client mount.
for ((i=0;i<30;i++)); do
  if podman logs "$cid" 2>&1 | command grep "NFS SERVER INITIALIZED" >/dev/null; then break; fi
  sleep 1
done
printf 'NFS_DIRECT_BRIDGE_RPC_PROBE address=%s port=%s\n' "$PULSE_M4_NFS_ADDRESS" "$PULSE_M4_NFS_PORT"
timeout 3 bash -c 'exec 3<>/dev/tcp/"$1"/"$2"' probe "$PULSE_M4_NFS_ADDRESS" "$PULSE_M4_NFS_PORT"
podman exec "$cid" rpcinfo -n 2049 -t 127.0.0.1 100003 4
timeout 5 rpcinfo -n "$PULSE_M4_NFS_PORT" -t "$PULSE_M4_NFS_ADDRESS" 100003 4
podman exec "$cid" dpkg-query -W nfs-ganesha nfs-ganesha-vfs
printf 'NFS_SERVER_CID=%s loopback_port=%s parent_namespace=%s\n' "$cid" "$PULSE_M4_NFS_PORT" "$(readlink /proc/self/ns/mnt)"
export PULSE_M4_NFS_PARENT_MNT="/proc/$$/ns/mnt"
unshare --mount --propagation private bash "$repo/scripts/m4_nfs_faults.sh" --client
