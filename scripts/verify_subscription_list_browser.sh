#!/usr/bin/env bash
# Verify movie/TV subscription consumers with owned PG, Chrome and Vite assets.
set -euo pipefail
umask 077
repo="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
mkdir -p /data/temp
work="$(mktemp -d /data/temp/pulse-subscription-ui.XXXXXX)"
cleanup() {
 result=$?
 trap - EXIT
 case "$work" in /data/temp/pulse-subscription-ui.*) test ! -L "$work"; rm -rf -- "$work" ;; *) exit 98 ;; esac
 test ! -e "$work" || exit 1
 printf 'OWNED_SUBSCRIPTION_UI_TEMP_CLEANUP_VERIFIED\n'
 exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$work" TMP="$work" TEMP="$work"
export PULSE_SUBSCRIPTION_UI_REPO="$repo" PULSE_SUBSCRIPTION_UI_ASSETS="$work/assets"
export PULSE_UI_FIXTURE=1
export PULSE_SUBSCRIPTION_UI_ARTIFACT_DIR="${PULSE_SUBSCRIPTION_UI_ARTIFACT_DIR:-$work}"
cat > "$work/vite.config.mjs" <<'CONFIG'
import {createRequire} from 'node:module';
const load=createRequire(process.env.PULSE_SUBSCRIPTION_UI_REPO+'/web/package.json');
const {default:react}=await import(load.resolve('@vitejs/plugin-react'));
export default {
 root:process.env.PULSE_SUBSCRIPTION_UI_REPO+'/web',
 plugins:[react()],
 resolve:{alias:{'@':process.env.PULSE_SUBSCRIPTION_UI_REPO+'/web/src'}},
 build:{outDir:process.env.PULSE_SUBSCRIPTION_UI_ASSETS,emptyOutDir:true}
};
CONFIG
cat > "$work/web_ui_overlay.go" <<'GO'
package web
import("net/http";"os")
func Dist() http.FileSystem { return http.Dir(os.Getenv("PULSE_SUBSCRIPTION_UI_ASSETS")) }
GO
printf '{"Replace":{"%s/web/web.go":"%s/web_ui_overlay.go"}}\n' "$repo" "$work" > "$work/overlay.json"
export GOFLAGS="${GOFLAGS:+$GOFLAGS }-overlay=$work/overlay.json"
cd "$repo"
pnpm --dir web exec vite build --config "$work/vite.config.mjs"
bash scripts/with_podman_test_postgres.sh bash -c '
 set -euo pipefail
 go test -race ./internal/app -run "^TestUnifiedSubscriptionCookieWorkflow$" -count=1 -timeout=3m -v
 go test -race ./internal/httpapi -run "^TestTVMetadataBrowserHarness$" -count=1 -timeout=2m -v
'
