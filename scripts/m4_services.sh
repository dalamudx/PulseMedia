#!/usr/bin/env bash
# Own only the pulsemedia-m4 Compose project, never the existing dev services.
set -euo pipefail
umask 077
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
action="${1:-status}"
state_dir="${2:-${PULSE_M4_STATE_DIR:-}}"
case "$action" in up|status|down|check|faults|orphans) ;; *) printf 'Usage: %s up|status|down|check|faults|orphans [state-dir]\n' "$0" >&2; exit 2 ;; esac
if [[ -z "$state_dir" && "$action" == up ]]; then
  mkdir -p /data/temp
  state_dir="$(mktemp -d /data/temp/pulse-m4-services.XXXXXX)"
fi
if [[ -z "$state_dir" || "$(dirname -- "$state_dir")" != /data/temp || ! "$(basename -- "$state_dir")" =~ ^pulse-m4-services\.[A-Za-z0-9]+$ || -L "$state_dir" ]]; then
  printf 'Use the owned /data/temp/pulse-m4-services.* directory printed by up.\n' >&2
  exit 2
fi
export PULSE_M4_STATE_DIR="$state_dir"
compose_cmd=(podman compose --env-file /dev/null --project-directory "$repo_root" -f "$repo_root/compose.integration.yaml")

# Refuse to adopt or replace another owner's similarly named container.
for component in qbittorrent transmission jellyfin prowlarr postgres; do
  container="pulsemedia-m4-$component"
  if podman container exists "$container"; then
    owner="$(podman inspect --format '{{index .Config.Labels "io.pulsemedia.state"}}' "$container")"
    if [[ "$owner" != "$state_dir" ]]; then
      printf 'Container %s belongs to a different state directory.\n' "$container" >&2
      exit 1
    fi
  fi
done

if [[ "$action" == up ]]; then
  for directory in qbittorrent qbittorrent/qBittorrent transmission jellyfin jellyfin/config jellyfin/cache prowlarr postgres downloads media media/movies media/tv secrets; do
    if [[ -L "$state_dir/$directory" ]]; then
      printf 'Refusing symlink service directory.\n' >&2; exit 1
    fi
    mkdir -p -- "$state_dir/$directory"
  done
  for file in qbittorrent/qBittorrent/qBittorrent.conf transmission/settings.json secrets/postgres-password; do
    if [[ -L "$state_dir/$file" || ( -e "$state_dir/$file" && ! -f "$state_dir/$file" ) ]]; then
      printf 'Refusing unsafe service configuration file.\n' >&2; exit 1
    fi
  done
  if [[ ! -e "$state_dir/qbittorrent/qBittorrent/qBittorrent.conf" ]]; then
    install -o 1000 -g 1000 -m 600 "$repo_root/deploy/integration/qBittorrent.conf" "$state_dir/qbittorrent/qBittorrent/qBittorrent.conf"
  fi
  if [[ ! -e "$state_dir/transmission/settings.json" ]]; then
    install -o 1000 -g 1000 -m 600 "$repo_root/deploy/integration/transmission.json" "$state_dir/transmission/settings.json"
  fi
  for directory in qbittorrent qbittorrent/qBittorrent transmission jellyfin/config jellyfin/cache prowlarr downloads media media/movies media/tv; do
    chown 1000:1000 -- "$state_dir/$directory"
    chmod 700 -- "$state_dir/$directory"
  done
  if [[ ! -e "$state_dir/secrets/postgres-password" ]]; then
    (set -o noclobber; openssl rand -base64 36 > "$state_dir/secrets/postgres-password")
  fi
  chmod 600 -- "$state_dir/secrets/postgres-password"
  "${compose_cmd[@]}" up -d
elif [[ "$action" == down ]]; then
  "${compose_cmd[@]}" down --timeout 30
elif [[ "$action" == check || "$action" == faults || "$action" == orphans ]]; then
  check_dir="$(mktemp -d /data/temp/pulse-m4-check.XXXXXX)"
  cleanup_check() {
    result=$?
    trap - EXIT
    case "$check_dir" in /data/temp/pulse-m4-check.*) rm -rf -- "$check_dir" ;; *) exit 98 ;; esac
    test ! -e "$check_dir" || exit 1
    printf 'OWNED_M4_CHECK_TEMP_CLEANUP_VERIFIED\n'
    exit "$result"
  }
  trap cleanup_check EXIT
  export TMPDIR="$check_dir" TMP="$check_dir" TEMP="$check_dir"
  export PULSE_M4_SERVICES_DIR="$state_dir"
  if [[ "$action" == faults ]]; then
    mise exec -- bash scripts/with_podman_test_postgres.sh go test -race ./internal/app \
      -run '^TestM4RealDownloadR3Faults$' -count=1 -timeout=4m -v
    mise exec -- bash scripts/with_podman_test_postgres.sh env PULSE_M4_REAL_VARIANT_PERMITS=1 \
      go test -race ./internal/httpapi -run '^TestM4Real(CompleteTV|Upgrade)R3Permit$' -count=1 -timeout=8m -v
  elif [[ "$action" == orphans ]]; then
    mise exec -- bash scripts/with_podman_test_postgres.sh env PULSE_M4_EXTERNAL_ORPHANS=1 \
      go test -race ./internal/app/backup -run '^TestM4RealExternalOrphanRestore$' -count=1 -timeout=4m -v
  else
    mise exec -- go test -race ./internal/app ./internal/adapter/mediaserver/jellyfin ./internal/adapter/indexer/torznab \
      -run '^(TestM4RealDownloaders|TestJellyfinRealService|TestM4RealProwlarr)$' -count=1 -timeout=4m -v
  fi
else
  "${compose_cmd[@]}" ps
fi
printf 'M4_STATE_DIR=%s\n' "$state_dir"
printf 'Credentials and databases are retained with this service environment.\n'
