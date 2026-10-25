#!/usr/bin/env bash
set -euo pipefail
umask 077
ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$ROOT_DIR"
if [[ -z "${PULSE_DEV_CONTAINER_CLI:-}" ]]; then
    printf '未检测到容器引擎，跳过开发下载器；可在设置中添加已有下载器。\n'
    exit 0
fi
: "${PULSE_DEV_RUNTIME_DIR:?Start through task dev so container cleanup has an owner}"
case "$PULSE_DEV_RUNTIME_DIR" in
    /data/temp/pulse-dev.*) [[ -d "$PULSE_DEV_RUNTIME_DIR" && ! -L "$PULSE_DEV_RUNTIME_DIR" ]] ;;
    *) exit 2 ;;
esac
: "${PULSE_M4_QB_IMAGE:?Run through mise exec}"
: "${PULSE_M4_TR_IMAGE:?Run through mise exec}"
QB_PORT="${PULSE_DEV_QB_PORT:-8085}"
TR_PORT="${PULSE_DEV_TR_PORT:-9091}"
for port in "$QB_PORT" "$TR_PORT"; do
    if [[ ! "$port" =~ ^[0-9]{1,5}$ ]] || (( 10#$port < 1 || 10#$port > 65535 )); then
        printf '开发下载器端口无效。\n' >&2
        exit 2
    fi
done
mkdir -p "$PULSE_DEV_RUNTIME_DIR/qb/qBittorrent" "$PULSE_DEV_RUNTIME_DIR/tr" "$PULSE_DATA_DIR/downloads"
# These configurations are disposable and are never written over existing user files.
cat > "$PULSE_DEV_RUNTIME_DIR/qb/qBittorrent/qBittorrent.conf" <<CONFIG
[LegalNotice]
Accepted=true

[BitTorrent]
Session\DefaultSavePath=/downloads/

[Preferences]
WebUI\Address=*
WebUI\Port=$QB_PORT
WebUI\LocalHostAuth=false
WebUI\AuthSubnetWhitelist=127.0.0.1/32,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,::1/128,fc00::/7
WebUI\AuthSubnetWhitelistEnabled=true
CONFIG
cat > "$PULSE_DEV_RUNTIME_DIR/tr/settings.json" <<'CONFIG'
{"download-dir":"/downloads","rpc-enabled":true,"rpc-bind-address":"0.0.0.0","rpc-port":9091,"rpc-authentication-required":false,"rpc-whitelist-enabled":false,"rpc-host-whitelist-enabled":true,"rpc-host-whitelist":"localhost,127.0.0.1","port-forwarding-enabled":false}
CONFIG
for component in qb tr; do
    image="$PULSE_M4_QB_IMAGE"
    # qBittorrent validates the request origin, including the WebUI port.
    publish="127.0.0.1:$QB_PORT:$QB_PORT"
    extra=(-e "WEBUI_PORT=$QB_PORT")
    if [[ "$component" == tr ]]; then image="$PULSE_M4_TR_IMAGE"; publish="127.0.0.1:$TR_PORT:9091"; extra=(); fi
    "$PULSE_DEV_CONTAINER_CLI" run -d --name "pulsemedia-dev-$component-${PULSE_DEV_RUNTIME_DIR##*.}" \
        --cidfile "$PULSE_DEV_RUNTIME_DIR/$component.cid" --label "io.pulsemedia.dev=$PULSE_DEV_RUNTIME_DIR" \
        -e "PUID=$(id -u)" -e "PGID=$(id -g)" -e TZ=UTC "${extra[@]}" \
        -p "$publish" -v "$PULSE_DEV_RUNTIME_DIR/$component:/config:Z" \
        -v "$PULSE_DATA_DIR/downloads:/downloads:z" "$image" >/dev/null
done
for attempt in {1..30}; do
    if curl --fail --silent --max-time 1 "http://127.0.0.1:$QB_PORT/api/v2/app/version" >/dev/null &&
        [[ "$(curl --silent --max-time 1 --output /dev/null --write-out '%{http_code}' "http://127.0.0.1:$TR_PORT/transmission/rpc")" == 409 ]]; then
        printf '本次开发下载器就绪：qBittorrent http://127.0.0.1:%s；Transmission http://127.0.0.1:%s。\n' "$QB_PORT" "$TR_PORT"
        exit 0
    fi
    sleep 1
done
printf '开发下载器未就绪，停止本次启动。\n' >&2
exit 1
