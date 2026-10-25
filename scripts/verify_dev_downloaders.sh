#!/usr/bin/env bash
set -euo pipefail
umask 077
repo="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$repo"
export PULSE_DEV_CONTAINER_CLI="$(command -v podman || command -v docker || true)"
[[ -n "$PULSE_DEV_CONTAINER_CLI" ]] || { printf 'A container engine is required.\n' >&2; exit 1; }
export PULSE_DEV_RUNTIME_DIR="$(mktemp -d /data/temp/pulse-dev.XXXXXX)"
export PULSE_DATA_DIR="$PULSE_DEV_RUNTIME_DIR/data"
export TMPDIR="$PULSE_DEV_RUNTIME_DIR" TMP="$PULSE_DEV_RUNTIME_DIR" TEMP="$PULSE_DEV_RUNTIME_DIR"
cleanup() {
    result=$?
    trap - EXIT INT TERM
    failed=0
    for component in qb tr; do
        cidfile="$PULSE_DEV_RUNTIME_DIR/$component.cid"
        [[ -s "$cidfile" ]] || continue
        cid="$(<"$cidfile")"
        if [[ ! "$cid" =~ ^[a-f0-9]{64}$ ]] ||
            ! owner="$("$PULSE_DEV_CONTAINER_CLI" inspect --format '{{index .Config.Labels "io.pulsemedia.dev"}}' "$cid" 2>/dev/null)" ||
            [[ "$owner" != "$PULSE_DEV_RUNTIME_DIR" ]]; then
            failed=1
            continue
        fi
        "$PULSE_DEV_CONTAINER_CLI" stop --time 15 "$cid" >/dev/null || failed=1
        "$PULSE_DEV_CONTAINER_CLI" rm "$cid" >/dev/null || failed=1
        if "$PULSE_DEV_CONTAINER_CLI" inspect "$cid" >/dev/null 2>&1; then failed=1; fi
    done
    if [[ "$failed" != 0 ]]; then printf 'Cleanup failed; retained evidence: %s\n' "$PULSE_DEV_RUNTIME_DIR" >&2; exit 1; fi
    case "$PULSE_DEV_RUNTIME_DIR" in
        /data/temp/pulse-dev.*) test ! -L "$PULSE_DEV_RUNTIME_DIR"; rm -rf -- "$PULSE_DEV_RUNTIME_DIR" ;;
        *) exit 98 ;;
    esac
    test ! -e "$PULSE_DEV_RUNTIME_DIR" || exit 1
    printf 'DEV_NATIVE_OWNED_RESOURCES_AND_TEMP_CLEANUP_VERIFIED\n'
    exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
read -r PULSE_DEV_QB_PORT PULSE_DEV_TR_PORT < <(node -e '
const net=require("node:net"),a=net.createServer(),b=net.createServer();
a.listen(0,"127.0.0.1",()=>b.listen(0,"127.0.0.1",()=>{console.log(a.address().port,b.address().port);a.close();b.close();}));')
export PULSE_DEV_QB_PORT PULSE_DEV_TR_PORT
bash scripts/ensure-dev-downloaders.sh
node <<'JS'
const assert = require('node:assert/strict');
(async () => {
    const qb = await fetch(`http://127.0.0.1:${process.env.PULSE_DEV_QB_PORT}/api/v2/app/preferences`, {signal:AbortSignal.timeout(5000)});
    assert.equal(qb.status, 200);
    const savePath = (await qb.json()).save_path;
    assert.ok(savePath === '/downloads' || savePath === '/downloads/');
    console.log('DEV_NATIVE_QB_PASSED default_download_path=EXACT host_and_webui_port=MATCH');
    const base = `http://127.0.0.1:${process.env.PULSE_DEV_TR_PORT}/transmission/rpc`;
    const probe = await fetch(base, {signal:AbortSignal.timeout(5000)});
    assert.equal(probe.status, 409);
    const response = await fetch(base, {method:'POST', signal:AbortSignal.timeout(5000), headers:{
        'X-Transmission-Session-Id':probe.headers.get('X-Transmission-Session-Id'), 'Content-Type':'application/json'
    }, body:JSON.stringify({method:'session-get'})});
    assert.equal(response.status, 200);
    assert.equal((await response.json()).arguments['download-dir'], '/downloads');
    console.log('DEV_NATIVE_TRANSMISSION_PASSED default_download_path=EXACT session_negotiation=TRUE');
})().catch(() => { console.error('Development downloader native contract failed.'); process.exitCode=1; });
JS
for component in qb tr; do
    cid="$(<"$PULSE_DEV_RUNTIME_DIR/$component.cid")"
    expected="$PULSE_DEV_QB_PORT"
    inside="$PULSE_DEV_QB_PORT"
    if [[ "$component" == tr ]]; then expected="$PULSE_DEV_TR_PORT"; inside=9091; fi
    test "$("$PULSE_DEV_CONTAINER_CLI" port "$cid" "$inside/tcp")" = "127.0.0.1:$expected"
    source_path="$("$PULSE_DEV_CONTAINER_CLI" inspect --format '{{range .Mounts}}{{if eq .Destination "/downloads"}}{{.Source}}{{end}}{{end}}' "$cid")"
    test "$source_path" = "$PULSE_DATA_DIR/downloads"
    "$PULSE_DEV_CONTAINER_CLI" exec "$cid" test -w /downloads
    printf 'DEV_NATIVE_BOUNDARY_PASSED component=%s loopback_only=TRUE data_root=OWNED_TEMP writable=TRUE\n' "$component"
done
printf 'DEV_NATIVE_PASSED components=2\n'
