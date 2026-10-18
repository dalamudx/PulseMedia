#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

# 导出可配置的环境变量（具备默认值兜底，支持由外部环境直接注入）
if [ "${PULSE_HTTP_ADDR:-}" = "0.0.0.0:8080" ] || [ -z "${PULSE_HTTP_ADDR:-}" ]; then
    export PULSE_HTTP_ADDR="0.0.0.0:8088"
fi
if [ "${PULSE_BACKEND_URL:-}" = "http://127.0.0.1:8080" ] || [ -z "${PULSE_BACKEND_URL:-}" ]; then
    export PULSE_BACKEND_URL="http://127.0.0.1:8088"
fi
export PULSE_LOG_LEVEL="${PULSE_LOG_LEVEL:-debug}"
export PULSE_DATA_DIR="${PULSE_DATA_DIR:-.data}"
export PULSE_DATABASE_URL="${PULSE_DATABASE_URL:-postgres://postgres@127.0.0.1:5433/pulsemedia?sslmode=disable}"
export PULSE_POSTGRES_PORT="${PULSE_POSTGRES_PORT:-5433}"
export PGPASSWORD="${PGPASSWORD:-}"
export VITE_HOST="${VITE_HOST:-0.0.0.0}"
export VITE_PORT="${VITE_PORT:-3000}"
export PULSE_BACKEND_URL="${PULSE_BACKEND_URL:-http://127.0.0.1:8088}"
export PULSE_DEV_QB_PORT="${PULSE_DEV_QB_PORT:-8085}"
export PULSE_DEV_TR_PORT="${PULSE_DEV_TR_PORT:-9091}"

# 1. 自动确保本地开发数据库启动与迁移完成
"$ROOT_DIR/scripts/ensure-dev-db.sh"

# 2. 自动启动 qBittorrent 与 Transmission 开发测试容器
"$ROOT_DIR/scripts/ensure-dev-downloaders.sh"

echo ""
echo "=================================================================="
echo "⚡ PulseMedia 一键开发热载环境已就绪"
echo "👉 前端界面 (Vite 热重载):   http://${VITE_HOST}:${VITE_PORT} (代理目标: ${PULSE_BACKEND_URL})"
echo "👉 后端 API (Air 热重载):    http://${PULSE_HTTP_ADDR}"
echo "👉 数据库连接:               ${PULSE_DATABASE_URL}"
echo "👉 测试下载器 (qBittorrent):  http://127.0.0.1:${PULSE_DEV_QB_PORT} (WebUI 本地免密 / 端口 ${PULSE_DEV_QB_PORT})"
echo "👉 测试下载器 (Transmission): http://127.0.0.1:${PULSE_DEV_TR_PORT} (账号: admin / 密码: adminadmin)"
echo "💡 提示: 按 Ctrl+C 停止时将自动优雅停止并销毁 qb 与 tr 容器"
echo "=================================================================="
echo ""

cleanup() {
    echo ""
    echo "正在停止前端和后端开发服务..."
    fuser -k "${VITE_PORT}/tcp" 2>/dev/null || true
    fuser -k "${PULSE_HTTP_ADDR##*:}/tcp" 2>/dev/null || true
    kill $(jobs -p) 2>/dev/null || true

    echo "正在优雅停止并销毁开发下载器测试容器 (qb & tr)..."
    CONTAINER_CLI="$(which podman 2>/dev/null || which docker 2>/dev/null || true)"
    if [ -n "$CONTAINER_CLI" ]; then
        $CONTAINER_CLI stop -t 2 pulsemedia-dev-qb pulsemedia-dev-tr 2>/dev/null || true
        $CONTAINER_CLI rm -f pulsemedia-dev-qb pulsemedia-dev-tr 2>/dev/null || true
        echo "开发下载器测试容器已优雅销毁。"
    fi
    echo "开发环境已完全安全退出。"
}
trap cleanup EXIT INT TERM

# 启动前端 Vite 热重载服务
(cd web && pnpm dev --host "$VITE_HOST" --port "$VITE_PORT") &

# 启动后端 Air 热重载服务
mise exec -- air
