#!/usr/bin/env bash
set -euo pipefail
mkdir -p /data/temp
pulse_reuse_tmp=$(mktemp -d /data/temp/pulse-reuse-ui.XXXXXX)
chmod 755 "$pulse_reuse_tmp"
cleanup() {
  pulse_reuse_status=$?
  case "$pulse_reuse_tmp" in /data/temp/pulse-reuse-ui.*) rm -rf -- "$pulse_reuse_tmp" ;; *) exit 98 ;; esac
  test ! -e "$pulse_reuse_tmp" || exit 1
  printf 'OWNED_REUSE_TEMP_CLEANUP_VERIFIED\n'
  exit "$pulse_reuse_status"
}
trap cleanup EXIT
export TMPDIR="$pulse_reuse_tmp" TMP="$pulse_reuse_tmp" TEMP="$pulse_reuse_tmp"
export PULSE_REUSE_REPO="$PWD" PULSE_REUSE_TASK_ROOT="$pulse_reuse_tmp"
export PULSE_TEST_TEMP_ROOT="$pulse_reuse_tmp"
export PULSE_REUSE_BROWSER=1 PULSE_REUSE_ASSET_DIR="$pulse_reuse_tmp/assets"
export PULSE_REUSE_SCREENSHOT_DIR="${PULSE_REUSE_SCREENSHOT_DIR:-$pulse_reuse_tmp}"
cat > "$pulse_reuse_tmp/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const load=createRequire(process.env.PULSE_REUSE_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
export default {
  root:process.env.PULSE_REUSE_REPO+'/web',
  envDir:process.env.PULSE_REUSE_TASK_ROOT,
  plugins:[react()],
  resolve:{alias:{'@':process.env.PULSE_REUSE_REPO+'/web/src'}},
  build:{outDir:process.env.PULSE_REUSE_ASSET_DIR,emptyOutDir:true}
};
CONFIG
pnpm --dir web exec tsc --noEmit
pnpm --dir web exec vite build --config "$pulse_reuse_tmp/vite.config.mjs"
bash scripts/with_test_postgres.sh go test -race ./internal/store/transfer ./internal/app -run '^TestLibraryReuse' -count=1 -v -timeout=4m
