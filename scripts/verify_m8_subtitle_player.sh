#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
test "$#" = 0
if [[ ${PULSE_M8_PLAYER_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M8_PLAYER_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m8_subtitle_player.sh
fi
: "${PULSE_TEST_DATABASE_URL:?owned PG required}"
: "${PULSE_M8_MPV_BASE_IMAGE:?run via mise exec}"
: "${PULSE_M8_MPV_PACKAGE_VERSION:?run via mise exec}"
[[ $PULSE_M8_MPV_BASE_IMAGE =~ ^docker.io/library/node@sha256:[a-f0-9]{64}$ ]]
[[ $PULSE_M8_MPV_PACKAGE_VERSION =~ ^[0-9][0-9a-z.+~-]*$ ]]
owned=$(mktemp -d /data/temp/pulse-m8-player.XXXXXX)
image="localhost/pulse-m8-player:${owned##*.}"
cleanup(){
 rc=$?; trap - EXIT
 if podman image exists "$image"; then podman rmi "$image" >/dev/null; fi
 if podman image exists "$image"; then echo M8_PLAYER_IMAGE_CLEANUP_FAILED >&2; exit 1; fi
 case "$owned" in /data/temp/pulse-m8-player.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned"
 echo M8_PLAYER_OWNED_IMAGE_AND_MEDIA_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" XDG_CACHE_HOME="$owned/cache"
cat > "$owned/Containerfile" <<EOF
FROM $PULSE_M8_MPV_BASE_IMAGE
ENV TMPDIR=/data/temp TMP=/data/temp TEMP=/data/temp XDG_CACHE_HOME=/data/temp/cache
RUN mkdir -p /data/temp/apt/lists/partial /data/temp/apt/cache/archives/partial && apt-get -o Dir::State::Lists=/data/temp/apt/lists -o Dir::Cache=/data/temp/apt/cache update && apt-get -o Dir::State::Lists=/data/temp/apt/lists -o Dir::Cache=/data/temp/apt/cache install -y --no-install-recommends mpv=$PULSE_M8_MPV_PACKAGE_VERSION && rm -rf -- /data/temp/apt
ENTRYPOINT ["mpv"]
EOF
podman build --layers=false --force-rm -t "$image" -f "$owned/Containerfile" "$owned" > "$owned/build.log" 2>&1 || { tail -n 35 "$owned/build.log"; exit 1; }
podman run --rm --network=none --cap-drop=ALL --security-opt=no-new-privileges --read-only "$image" --version
export PULSE_M8_SUBTITLE_PLAYER_ACCEPTANCE=1 PULSE_M8_SUBTITLE_PLAYER_IMAGE="$image" PULSE_M8_SUBTITLE_PLAYER_VIDEO="$owned/playable.mkv"
ffmpeg -v error -f lavfi -i testsrc2=size=320x180:rate=10 -t 8 -an -c:v mpeg4 -q:v 2 -f matroska "$PULSE_M8_SUBTITLE_PLAYER_VIDEO"
go test -race ./internal/app -run '^TestM8SubtitleNativePlayerPG$' -count=1 -v -timeout=6m 2>&1 | tee "$owned/player.log"
! grep -q -- '--- SKIP:' "$owned/player.log"
test "$(grep -c '^--- PASS: TestM8SubtitleNativePlayerPG (' "$owned/player.log")" = 1
test "$(grep -c 'M8_SUBTITLE_PLAYER_PASS ' "$owned/player.log")" = 1
echo M8_SUBTITLE_REAL_PLAYER_ACCEPTANCE_PASS_parents=1_skipped=0_Player=mpv_App=REAL_PG=18.6_Network=OWNED_HTTP_live_shooter=false
