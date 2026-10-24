#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ ${PULSE_M9_REQUEST_BROWSER:-} != 1 ]]; then
  echo 'Set PULSE_M9_REQUEST_BROWSER=1 for owned Chrome/PostgreSQL media-request acceptance.' >&2
  exit 2
fi
if [[ ${PULSE_M9_REQUEST_OWNED_PG:-} != 1 ]]; then
  exec env PULSE_M9_REQUEST_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m9_media_requests_browser.sh
fi
: "${PULSE_TEST_DATABASE_URL:?private PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m9-request-browser.XXXXXX)
cleanup() {
  rc=$?
  trap - EXIT
  case "$owned" in
    /data/temp/pulse-m9-request-browser.*)
      test ! -L "$owned"
      test "$(realpath -- "$owned")" = "$owned"
      rm -rf -- "$owned"
      ;;
    *) exit 98 ;;
  esac
  test ! -e "$owned" || exit 1
  echo M9_REQUEST_CHROME_OWNED_TEMP_CLEANUP_VERIFIED
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache" XDG_CACHE_HOME="$owned/cache" XDG_CONFIG_HOME="$owned/config"
export PULSE_M9_REQUEST_REPO="$repo" PULSE_M9_REQUEST_ASSETS="$owned/web" PULSE_M9_REQUEST_SCREENSHOTS="$owned/screenshots"
# Main reads all six local images and touches the inspected receipt within
# 150 seconds of READY. Set to 0 only for an explicitly nonvisual diagnostic run.
export PULSE_M9_REQUEST_REVIEW="${PULSE_M9_REQUEST_REVIEW:-1}"
if [[ "$PULSE_M9_REQUEST_REVIEW" != 0 && "$PULSE_M9_REQUEST_REVIEW" != 1 ]]; then
  echo 'PULSE_M9_REQUEST_REVIEW must be 0 or 1.' >&2
  exit 2
fi
mkdir -p "$GOTMPDIR" "$PULSE_M9_REQUEST_SCREENSHOTS"
cat > "$owned/vite.config.mjs" <<'CONFIG'
import { createRequire } from 'node:module';
const load = createRequire(process.env.PULSE_M9_REQUEST_REPO + '/web/package.json');
const { default: react } = await import(load.resolve('@vitejs/plugin-react'));
let bridges = 0;
const authBridge = {
  name: 'm9-owned-requests-auth-status-observer', enforce: 'pre',
  transform(source, id) {
    if (!id.endsWith('/features/auth/AuthContext.tsx')) return null;
    const anchor = 'return <AuthContext.Provider';
    if (source.split(anchor).length !== 2) throw Error('AuthContext bridge anchor changed');
    bridges++;
    return source.replace(anchor, 'window.__m9RequestsAuth={checkStatus};\n  ' + anchor);
  },
  closeBundle() { if (bridges !== 1) throw Error('Expected one real AuthContext observer'); },
};
export default {
  root: process.env.PULSE_M9_REQUEST_REPO + '/web', envDir: process.env.TMPDIR,
  plugins: [authBridge, react()], cacheDir: process.env.TMPDIR + '/vite-cache',
  resolve: { alias: { '@': process.env.PULSE_M9_REQUEST_REPO + '/web/src' } },
  build: { outDir: process.env.PULSE_M9_REQUEST_ASSETS, emptyOutDir: true },
};
CONFIG
mise exec -- pnpm --dir web exec vite build --config "$owned/vite.config.mjs" > "$owned/build.log" 2>&1 || { tail -n 30 "$owned/build.log"; exit 1; }
mise exec -- go test -race ./internal/app -run '^TestM9MediaRequestsChromePG$' -count=1 -v -timeout=8m 2>&1 | tee "$owned/acceptance.log"
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then
  echo M9_REQUEST_BROWSER_UNEXPECTED_SKIP >&2
  exit 1
fi
test "$(grep -c '^--- PASS: TestM9MediaRequestsChromePG (' "$owned/acceptance.log")" = 1
test "$(grep -c '^M9_REQUEST_CHROME_PASS checks=' "$owned/acceptance.log")" = 1
for name in media-requests-1440 media-requests-768 media-requests-375 media-requests-320 media-requests-admin media-requests-recovery; do
  test -s "$PULSE_M9_REQUEST_SCREENSHOTS/$name.png"
done
if [[ "$PULSE_M9_REQUEST_REVIEW" == 1 ]]; then test -f "$PULSE_M9_REQUEST_SCREENSHOTS/inspected"; fi
echo M9_REQUEST_NATIVE_CHROME_ACCEPTANCE_PASS_parents=1_skipped=0
