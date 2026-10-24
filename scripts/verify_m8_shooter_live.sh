#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# -gt 1 || ! ${1:-protocol} =~ ^(protocol|sintel)$ ]]; then echo 'Usage: verify_m8_shooter_live.sh [protocol|sintel]' >&2; exit 2; fi
sample=${1:-protocol}
owned=$(mktemp -d /data/temp/pulse-m8-shooter-live.XXXXXX)
cleanup(){ rc=$?; trap - EXIT; test ! -L "$owned"; rm -rf -- "$owned"; test ! -e "$owned"; echo M8_LIVE_SHOOTER_OWNED_TEMP_CLEANUP_VERIFIED; exit "$rc"; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
module_cache=$(go env GOMODCACHE)
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache" GOMODCACHE="$owned/modules" GOPROXY="file://$module_cache/cache/download"
mkdir -p "$GOTMPDIR" "$GOCACHE" "$GOMODCACHE"
if [[ $sample == sintel ]]; then
 hash=''
 for offset in 4096 786727060 393363530 1180082398; do
  end=$((offset+4095))
  if status=$(curl --silent --show-error --connect-timeout 5 --max-time 12 --max-filesize 4096 --range "$offset-$end" --dump-header "$owned/headers" --output "$owned/block" --write-out '%{http_code}' "https://download.blender.org/durian/movies/Sintel.2010.1080p.mkv?block=$offset&acceptance=${owned##*.}"); then
   if [[ $status != 206 ]] || ! grep -iq "^content-range: bytes $offset-$end/1180090590" "$owned/headers" || [[ $(wc -c < "$owned/block") != 4096 ]]; then
    echo "M8_LIVE_SHOOTER_UNVERIFIED stage=PUBLIC_SOURCE_RANGE offset=$offset http=$status contract=EXACT_RANGE_REQUIRED" >&2
    exit 1
   fi
  else
   range_rc=$?
   echo "M8_LIVE_SHOOTER_UNVERIFIED stage=PUBLIC_SOURCE_RANGE offset=$offset http=$status curl_exit=$range_rc native_query=NOT_SENT" >&2
   exit "$range_rc"
  fi
  block_hash=$(md5sum "$owned/block" | cut -d ' ' -f 1)
  if test -z "$hash"; then hash="$block_hash"; else hash="$hash;$block_hash"; fi
 done
 echo M8_LIVE_SHOOTER_MEDIA_HASH_VERIFIED_source=Sintel_CC_BY_full_bytes=1180090590_ranges=4_bytes_read=16384
 export PULSE_M8_SHOOTER_LIVE_ACCEPTANCE=1 PULSE_M8_SHOOTER_LIVE_HASH="$hash"
 test_name=TestShooterPublicSintelLive
else
 export PULSE_M8_SHOOTER_PROTOCOL_ACCEPTANCE=1
 test_name=TestShooterPublishedProtocolLive
 echo M8_LIVE_SHOOTER_PROTOCOL_SAMPLE_source=Subliminal_2.1.0_physical_video_NOT_DOWNLOADED
fi
go test -race ./internal/adapter/subtitle/shooter -run "^$test_name$" -count=1 -v -timeout=90s 2>&1 | tee "$owned/acceptance.log"
! grep -q -- '--- SKIP:' "$owned/acceptance.log"
test "$(grep -c "^--- PASS: $test_name (" "$owned/acceptance.log")" = 1
test "$(grep -c 'M8_LIVE_SHOOTER_PASS ' "$owned/acceptance.log")" = 1
echo "M8_SHOOTER_REAL_MATCH_AND_DOWNLOAD_ACCEPTANCE_PASS parents=1 skipped=0 sample=$sample fixture=false"
