#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

CONTAINER_CLI="$(which podman 2>/dev/null || which docker 2>/dev/null || true)"

if [ -z "$CONTAINER_CLI" ]; then
    echo "⚠️ 未检测到 podman 或 docker，跳过下载器测试容器启动。"
    exit 0
fi

QB_PORT="${PULSE_DEV_QB_PORT:-8085}"
TR_PORT="${PULSE_DEV_TR_PORT:-9091}"

# 准备测试配置目录与下载目录
mkdir -p "$ROOT_DIR/.data/dev-qb-config/qBittorrent"
mkdir -p "$ROOT_DIR/.data/dev-tr-config"
mkdir -p "$ROOT_DIR/.data/downloads"

# 初始化 qBittorrent 配置（开放本地网段免认证以供开发测试零门槛直连）
if [ ! -f "$ROOT_DIR/.data/dev-qb-config/qBittorrent/qBittorrent.conf" ]; then
    cat << EOF > "$ROOT_DIR/.data/dev-qb-config/qBittorrent/qBittorrent.conf"
[LegalNotice]
Accepted=true

[Preferences]
ConnectionPortRangeMin=6881
DownloadsSavePath=/downloads/
WebUI\\Address=*
WebUI\\AuthSubnetWhitelist=0.0.0.0/0, 127.0.0.1/32, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16
WebUI\\AuthSubnetWhitelistEnabled=true
WebUI\\CSRFProtection=false
WebUI\\ClickjackingProtection=false
WebUI\\HostHeaderValidation=false
WebUI\\LocalHostAuth=false
WebUI\\Port=${QB_PORT}
WebUI\\Username=admin
EOF
fi

# 1. 检查并启动 qBittorrent 容器
if ! $CONTAINER_CLI ps --format '{{.Names}}' | grep -q "^pulsemedia-dev-qb$"; then
    echo "启动 qBittorrent 开发测试容器 (端口 ${QB_PORT})..."
    $CONTAINER_CLI rm -f pulsemedia-dev-qb 2>/dev/null || true
    $CONTAINER_CLI run -d --name pulsemedia-dev-qb \
        -e PUID=0 -e PGID=0 -e TZ=Asia/Shanghai -e WEBUI_PORT="${QB_PORT}" \
        -p "${QB_PORT}:${QB_PORT}" \
        -v "$ROOT_DIR/.data/dev-qb-config:/config:Z" \
        -v "$ROOT_DIR/.data/downloads:/downloads:Z" \
        docker.io/linuxserver/qbittorrent:latest >/dev/null
fi

# 2. 检查并启动 Transmission 容器
if ! $CONTAINER_CLI ps --format '{{.Names}}' | grep -q "^pulsemedia-dev-tr$"; then
    echo "启动 Transmission 开发测试容器 (端口 ${TR_PORT})..."
    $CONTAINER_CLI rm -f pulsemedia-dev-tr 2>/dev/null || true
    $CONTAINER_CLI run -d --name pulsemedia-dev-tr \
        -e PUID=0 -e PGID=0 -e TZ=Asia/Shanghai -e USER=admin -e PASS=adminadmin \
        -p "${TR_PORT}:${TR_PORT}" \
        -v "$ROOT_DIR/.data/dev-tr-config:/config:Z" \
        -v "$ROOT_DIR/.data/downloads:/downloads:Z" \
        docker.io/linuxserver/transmission:latest >/dev/null
fi

# 3. 等待端口就绪
for i in {1..15}; do
    if curl -s "http://127.0.0.1:${QB_PORT}/api/v2/app/version" >/dev/null 2>&1 || curl -s "http://127.0.0.1:${QB_PORT}" >/dev/null 2>&1; then
        break
    fi
    sleep 0.5
done

# 4. 如果本地 PG 数据库已运行，自动在 download_client 表中注册或更新测试下载器
PG_PORT="${PULSE_POSTGRES_PORT:-5433}"
if command -v psql >/dev/null 2>&1 && pg_isready -h 127.0.0.1 -p "$PG_PORT" >/dev/null 2>&1; then
    psql -h 127.0.0.1 -p "$PG_PORT" -U postgres -d pulsemedia >/dev/null 2>&1 << 'SQL' || true
INSERT INTO download_client (id, name, client_type, url, username, password_encrypted, created_at, updated_at)
VALUES 
  ('018e0000-0000-7000-8000-000000000011', '开发测试 qBittorrent', 'qbittorrent', 'http://127.0.0.1:8085', 'admin', '\x01234567890123456789012345678901', NOW(), NOW()),
  ('018e0000-0000-7000-8000-000000000012', '开发测试 Transmission', 'transmission', 'http://127.0.0.1:9091', 'admin', '\x01234567890123456789012345678901', NOW(), NOW())
ON CONFLICT (id) DO UPDATE SET url = EXCLUDED.url, name = EXCLUDED.name;
SQL
fi

echo "开发下载器测试容器已就绪 (qb: http://127.0.0.1:${QB_PORT}, tr: http://127.0.0.1:${TR_PORT})。"
