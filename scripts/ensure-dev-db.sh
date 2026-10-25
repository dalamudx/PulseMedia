#!/usr/bin/env bash
set -euo pipefail

DB_PORT="${PULSE_POSTGRES_PORT:-5433}"
export PGPASSWORD="${PGPASSWORD:-}"
DB_USER="postgres"
DB_NAME="pulsemedia"
PG_DATA_DIR="/tmp/pulsemedia-local-dev-pg/data"
PG_BIN="/opt/mise/installs/postgres/18.6/bin"
[ -d "$PG_BIN" ] || PG_BIN="/root/.local/share/mise/installs/postgres/18.6/bin"
LD_PATH="/opt/mise/installs/postgres/18.6/lib"

echo "检查本地 PostgreSQL (端口 $DB_PORT)..."

# 检查数据库是否已在端口运行
if ! runuser -u postgres -- "$PG_BIN/pg_isready" -p "$DB_PORT" -h 127.0.0.1 >/dev/null 2>&1; then
    echo "启动本地 PostgreSQL 18 服务..."
    mkdir -p "/tmp/pulsemedia-local-dev-pg"
    chown -R postgres:postgres "/tmp/pulsemedia-local-dev-pg"

    if [ ! -f "$PG_DATA_DIR/PG_VERSION" ]; then
        echo "初始化开发数据库集群..."
        runuser -u postgres -- env LD_LIBRARY_PATH="$LD_PATH" "$PG_BIN/initdb" -D "$PG_DATA_DIR" --no-sync --auth=trust >/dev/null
    fi

    runuser -u postgres -- env LD_LIBRARY_PATH="$LD_PATH" "$PG_BIN/pg_ctl" -D "$PG_DATA_DIR" \
        -o "-p $DB_PORT -h 127.0.0.1" \
        -l "/tmp/pulsemedia-local-dev-pg/pg.log" start
    
    sleep 1
fi

# 确保 pulsemedia 数据库存在
runuser -u postgres -- env LD_LIBRARY_PATH="$LD_PATH" "$PG_BIN/psql" -p "$DB_PORT" -h 127.0.0.1 -c "CREATE DATABASE $DB_NAME;" 2>/dev/null || true

# 自动执行迁移
MIGRATE_URL="postgres://postgres@127.0.0.1:$DB_PORT/$DB_NAME?sslmode=disable"
echo "自动检查并应用数据库迁移..."
mise exec -- goose -dir migrations postgres "$MIGRATE_URL" up >/dev/null

echo "开发数据库就绪（连接信息不输出）。"
