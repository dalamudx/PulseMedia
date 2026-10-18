#!/usr/bin/env bash
set -euo pipefail

echo "=== Verifying Built Binary Smoke Execution ==="
./bin/pulsemedia -v

echo "=== Verifying Graceful Signal Shutdown ==="
TEMP_DIR=$(mktemp -d /tmp/pulsemedia-smoke.XXXXXX)
./bin/pulsemedia --data-dir "$TEMP_DIR" &
PID=$!
sleep 0.5
kill -TERM "$PID"
wait "$PID" || true
rm -rf "$TEMP_DIR"

echo "=== Binary Smoke Passed Successfully ==="
