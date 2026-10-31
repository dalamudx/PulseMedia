#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-}
[[ $# == 1 && $mode =~ ^(backend|browser)$ ]] || { printf '%s\n' 'usage: verify_core_media.sh backend|browser' >&2; exit 2; }
if [[ $mode == backend && -z ${PULSE_TEST_DATABASE_URL:-} ]]; then
  exec bash scripts/with_podman_test_postgres.sh bash scripts/verify_core_media.sh backend
fi
owned=$(mktemp -d /data/temp/pulse-core-next.XXXXXX)
cleanup() {
  local status=$?
  if [[ -n ${PULSE_CORE_MEDIA_EVIDENCE:-} ]]; then
    case "$PULSE_CORE_MEDIA_EVIDENCE" in /data/temp/*) ;; *) exit 2 ;; esac
    test -d "$PULSE_CORE_MEDIA_EVIDENCE"
    for artifact in manifest.log results.log frontend.log; do
      if [[ -f "$owned/$artifact" ]]; then cp -- "$owned/$artifact" "$PULSE_CORE_MEDIA_EVIDENCE/$mode-$artifact"; fi
    done
  fi
  rm -rf -- "$owned"
  test ! -e "$owned" || exit 1
  printf '%s\n' CORE_MEDIA_OWNED_TEMP_CLEANUP_VERIFIED
  exit "$status"
}
trap cleanup EXIT
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" PULSE_TEST_TEMP_ROOT="$owned"
export XDG_CONFIG_HOME="$owned/chrome-config" XDG_CACHE_HOME="$owned/chrome-cache"
chrome=${PULSE_TEST_BROWSER_EXECUTABLE:-/usr/bin/google-chrome}
test -x "$chrome"
export PULSE_TEST_BROWSER_EXECUTABLE="$chrome" PULSE_CHROME_PATH="$chrome" PULSE_CHROME_BIN="$chrome" CHROME_BIN="$chrome"
if [[ $mode == browser ]]; then
  export PULSE_DIAGNOSTIC_EVIDENCE="$owned"
  for probe in lazy-routes download-global-rates movie-discovery external-file-review tv-episode-upgrade tv-episode-upgrade-workspace metadata-diagnostics file-mapping-media-picker external-batch library-maintenance tv-episode-discovery-workspace delivery-history; do
    node "web/tests/${probe}-browser.mjs"
  done
  if ! (cd web && node --test --test-reporter=tap tests/*.test.mjs) > "$owned/frontend.log" 2>&1; then tail -n 100 "$owned/frontend.log"; exit 1; fi
  if grep -E '^# (fail|cancelled|skipped) [1-9]' "$owned/frontend.log"; then exit 1; fi
  grep -E '^# (tests|pass|fail|skipped) ' "$owned/frontend.log"
  printf '%s\n' CORE_MEDIA_BROWSER_PASS
else
  export PULSE_M8_UNTRACKED_ORGANIZATION_ACCEPTANCE=1 PULSE_MP29_CHROME=1
  pattern='^(TestHistoryRetention|TestPhysicalConnection|TestSubscriptionDetails|TestRetiredLegacyEndpoints|TestDeliveryHistory|TestFileMappingOptions|TestFileMappingMedia|TestExternalBatch|TestLibraryMaintenance|TestMaintenanceNative|TestManagedSidecar|TestMP29ManagedSidecar|TestTVEpisodeDiscovery|TestMovieDiscovery|TestUntrackedReviewCanonical|TestUntrackedReviewScope|TestMP19|TestMP20|TestDownloadControlDedicated|TestTVEpisodeUpgrade|TestMetadataDiagnostic|TestEnrichmentDiagnostic|TestUntrackedCompanion)'
  packages=(
    ./internal/store/postgres ./internal/app ./internal/httpapi
    ./internal/store/download ./internal/adapter/torrent
    ./internal/adapter/metadata/tmdb ./internal/platform/rootfs
    ./internal/usecase/library ./internal/usecase/search ./internal/usecase/metadatadiagnostics
    ./internal/domain/media ./internal/domain/quality ./internal/ports
    ./internal/store/mediasidecar ./internal/usecase/metadatasidecar ./internal/usecase/librarymaintenance
  )
  go test -race -list "$pattern" "${packages[@]}" > "$owned/manifest.log"
  expected=$(grep -c '^Test' "$owned/manifest.log")
  test "$expected" -gt 0
  if ! go test -race -count=1 -timeout=20m -run "$pattern" -v "${packages[@]}" > "$owned/results.log" 2>&1; then
    grep -B 8 -A 3 -E -- '--- FAIL:|^FAIL[[:space:]]|^panic:|^fatal error:' "$owned/results.log" || true
    tail -n 35 "$owned/results.log"
    exit 1
  fi
  if grep -E '^[[:space:]]*--- (SKIP|FAIL):' "$owned/results.log"; then exit 1; fi
  passed=$(grep -c '^--- PASS:' "$owned/results.log")
  test "$passed" -eq "$expected"
  printf 'CORE_MEDIA_BACKEND_PASS parents=%s skips=0 race=1\n' "$passed"
fi
