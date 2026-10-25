#!/usr/bin/env bash
set -euo pipefail
umask 077
ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$ROOT_DIR"

export PULSE_HTTP_ADDR="${PULSE_HTTP_ADDR:-0.0.0.0:8088}"
export PULSE_BACKEND_URL="${PULSE_BACKEND_URL:-http://127.0.0.1:8088}"
export VITE_HOST="${VITE_HOST:-0.0.0.0}"
export VITE_PORT="${VITE_PORT:-3000}"
export PULSE_DEV_QB_PORT="${PULSE_DEV_QB_PORT:-8085}"
export PULSE_DEV_TR_PORT="${PULSE_DEV_TR_PORT:-9091}"
export PULSE_DATA_DIR="${PULSE_DATA_DIR:-$ROOT_DIR/.data}"
mkdir -p "$PULSE_DATA_DIR"
export PULSE_DATA_DIR="$(cd -- "$PULSE_DATA_DIR" && pwd -P)"
for program in setsid pnpm air; do
    command -v "$program" >/dev/null || { printf '缺少开发工具 %s，请通过 mise exec -- task dev 启动。\n' "$program" >&2; exit 1; }
done

mkdir -p /data/temp
PULSE_DEV_RUNTIME_DIR="$(mktemp -d /data/temp/pulse-dev.XXXXXX)"
export PULSE_DEV_RUNTIME_DIR
export TMPDIR="$PULSE_DEV_RUNTIME_DIR" TMP="$PULSE_DEV_RUNTIME_DIR" TEMP="$PULSE_DEV_RUNTIME_DIR"
PULSE_DEV_CONTAINER_CLI="$(command -v podman || command -v docker || true)"
export PULSE_DEV_CONTAINER_CLI
children=()
cleanup() {
    result=$?
    trap - EXIT INT TERM
    for child in "${children[@]}"; do kill -TERM -- "-$child" 2>/dev/null || true; done
    for child in "${children[@]}"; do wait "$child" 2>/dev/null || true; done
    cleanup_failed=0
    for component in qb tr; do
        cidfile="$PULSE_DEV_RUNTIME_DIR/$component.cid"
        [[ -s "$cidfile" ]] || continue
        container_id="$(<"$cidfile")"
        if [[ ! "$container_id" =~ ^[a-f0-9]{64}$ ]] || [[ -z "$PULSE_DEV_CONTAINER_CLI" ]] ||
            ! owner="$("$PULSE_DEV_CONTAINER_CLI" inspect --format '{{index .Config.Labels "io.pulsemedia.dev"}}' "$container_id" 2>/dev/null)" ||
            [[ "$owner" != "$PULSE_DEV_RUNTIME_DIR" ]]; then
            cleanup_failed=1
            continue
        fi
        if ! "$PULSE_DEV_CONTAINER_CLI" stop --time 15 "$container_id" >/dev/null ||
            ! "$PULSE_DEV_CONTAINER_CLI" rm "$container_id" >/dev/null; then
            cleanup_failed=1
        fi
    done
    if [[ "$cleanup_failed" != 0 ]]; then
        printf '开发资源清理未完成，保留身份记录：%s\n' "$PULSE_DEV_RUNTIME_DIR" >&2
        exit 1
    fi
    case "$PULSE_DEV_RUNTIME_DIR" in
        /data/temp/pulse-dev.*) test ! -L "$PULSE_DEV_RUNTIME_DIR"; rm -rf -- "$PULSE_DEV_RUNTIME_DIR" ;;
        *) exit 98 ;;
    esac
    test ! -e "$PULSE_DEV_RUNTIME_DIR" || exit 1
    printf '开发进程及本次下载器容器已清理；数据库和下载文件保留。\n'
    exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

"$ROOT_DIR/scripts/ensure-dev-db.sh"
"$ROOT_DIR/scripts/ensure-dev-downloaders.sh"
printf '前端：http://%s:%s；后端：http://%s\n' "$VITE_HOST" "$VITE_PORT" "$PULSE_HTTP_ADDR"
printf '数据库连接信息不输出；下载器请在设置中添加，凭据由应用加密保存。\n'
printf '按 Ctrl+C 停止本次开发服务。\n'

# Each child owns a separate session so cleanup reaches its build/runtime descendants.
setsid bash -c 'cd web && exec pnpm dev --host "$VITE_HOST" --port "$VITE_PORT" --strictPort' &
children+=("$!")
setsid air -c .air.toml --tmp_dir "$PULSE_DEV_RUNTIME_DIR/air" \
    --build.bin "$PULSE_DEV_RUNTIME_DIR/main" \
    --build.cmd "go build -o '$PULSE_DEV_RUNTIME_DIR/main' ./cmd/pulsemedia" &
children+=("$!")

# A failed frontend or backend stops its sibling rather than leaving a partial environment.
result=0
wait -n "${children[@]}" || result=$?
exit "$result"
