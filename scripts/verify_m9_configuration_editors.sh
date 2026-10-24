#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-all}
case "$mode" in frontend|browser|all) ;; *) echo 'usage: verify_m9_configuration_editors.sh [frontend|browser|all]' >&2; exit 2;; esac
owned=$(mktemp -d /data/temp/pulse-m9-editor-formal.XXXXXX)
cleanup(){ rc=$?;trap - EXIT;case "$owned" in /data/temp/pulse-m9-editor-formal.*) test ! -L "$owned";test "$(realpath -- "$owned")" = "$owned";rm -rf -- "$owned";; *) exit 98;; esac;test ! -e "$owned" || exit 1;echo M9_EDITOR_FORMAL_TEMP_CLEANUP_VERIFIED;exit "$rc"; }
trap cleanup EXIT
export TMPDIR="$owned" TMP="$owned" TEMP="$owned"
if [[ "$mode" != browser ]]; then
 mise exec -- node --test --test-reporter=tap web/tests/policy-editor-model.test.mjs web/tests/configuration-editors.test.mjs >"$owned/frontend.log" 2>&1 || { tail -n 100 "$owned/frontend.log";exit 1; }
 for receipt in '# tests 29' '# pass 29' '# fail 0' '# cancelled 0' '# skipped 0';do grep -Fxq "$receipt" "$owned/frontend.log" || { tail -n 100 "$owned/frontend.log";exit 1; };done
 mise exec -- task test-web
 echo M9_EDITOR_FRONTEND_PASS_parents=29_skipped=0
fi
if [[ "$mode" != frontend ]];then PULSE_M9_EDITOR_BROWSER=1 bash scripts/verify_m9_configuration_editors_browser.sh;fi
echo M9_EDITOR_FORMAL_ACCEPTANCE_PASS_mode="$mode"
