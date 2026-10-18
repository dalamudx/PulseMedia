#!/usr/bin/env bash
# Build a narrow artifact-only context with the mise-managed host toolchain.
set -euo pipefail
umask 077
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
: "${PULSE_CONTAINER_BASE_IMAGE:?Run via mise exec}"
image="${1:-localhost/pulsemedia:dev}"
arch="${2:-$(go env GOARCH)}"
case "$arch" in amd64|arm64) ;; *) printf 'Supported target architectures: amd64, arm64\n' >&2; exit 2 ;; esac
version="${PULSE_BUILD_VERSION:-dev}"
commit="$(bash "$repo_root/scripts/build_revision.sh" "$version" "$repo_root")"
work="$(mktemp -d /data/temp/pulse-container-build.XXXXXX)"
cleanup() {
  code=$?
  trap - EXIT
  rm -rf -- "$work"
  test ! -e "$work" || exit 98
  printf 'OWNED_CONTAINER_BUILD_CONTEXT_CLEANUP_VERIFIED\n'
  exit "$code"
}
trap cleanup EXIT
export TMPDIR="$work" TMP="$work" TEMP="$work"
mkdir -p "$work/src/web" "$work/context"
# Copy only compilation inputs, never local config, credentials or data.
cp -a -- "$repo_root/cmd" "$repo_root/internal" "$work/src/"
cp -- "$repo_root/go.mod" "$repo_root/go.sum" "$work/src/"
cp -- "$repo_root/web/web.go" "$work/src/web/"
pnpm --dir "$repo_root/web" exec tsc -b
pnpm --dir "$repo_root/web" exec vite build --outDir "$work/src/web/dist"
built="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
cd "$work/src"
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -buildvcs=false -trimpath \
  -ldflags "-s -w -X main.Version=$version -X main.Commit=$commit -X main.Date=$built" \
  -o "$work/context/pulsemedia" ./cmd/pulsemedia
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -buildvcs=false -trimpath \
  -o "$work/context/pulsebackup" ./cmd/pulsebackup
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -buildvcs=false -trimpath \
  -o "$work/context/pulsemigrate" ./cmd/pulsemigrate
chmod 0755 "$work/context/pulsemedia" "$work/context/pulsebackup" "$work/context/pulsemigrate"
cp -a -- "$repo_root/migrations" "$work/context/migrations"
cp -- "$repo_root/Dockerfile" "$work/context/Dockerfile"
if [[ "$version" != dev ]]; then
  final_commit="$(bash "$repo_root/scripts/build_revision.sh" "$version" "$repo_root")"
  if [[ "$final_commit" != "$commit" ]]; then
    printf 'Source revision changed during the build.\n' >&2
    exit 1
  fi
fi
podman build --platform "linux/$arch" \
  --build-arg "RUNTIME_IMAGE=$PULSE_CONTAINER_BASE_IMAGE" \
  --build-arg "VERSION=$version" --build-arg "COMMIT=$commit" \
  --tag "$image" "$work/context"
printf 'BUILT_IMAGE=%s PLATFORM=linux/%s\n' "$image" "$arch"
