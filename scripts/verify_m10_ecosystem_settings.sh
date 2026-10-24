#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-all}
case "$mode" in frontend|browser|all);;*)echo 'Usage: verify_m10_ecosystem_settings.sh [frontend|browser|all]' >&2;exit 2;;esac
owned=$(mktemp -d /data/temp/pulse-m10-ecosystem-settings.XXXXXX)
trap 'rc=$?;test ! -L "$owned";test "$(realpath -- "$owned")" = "$owned";rm -rf -- "$owned";test ! -e "$owned" || exit 1;echo M10_ECOSYSTEM_SETTINGS_FORMAL_TEMP_CLEANUP_VERIFIED;exit "$rc"' EXIT
export TMPDIR="$owned" TMP="$owned" TEMP="$owned"
if [[ "$mode" == frontend || "$mode" == all ]];then
 mise exec -- task test-web
 mise exec -- node --test --test-reporter=tap web/tests/configuration-editors.test.mjs web/tests/seeding-rule-input.test.mjs web/tests/notification-test-command.test.mjs >"$owned/frontend.log" 2>&1 || { tail -n 70 "$owned/frontend.log";exit 1; }
 for receipt in '# tests 33' '# pass 33' '# fail 0' '# cancelled 0' '# skipped 0';do grep -Fxq "$receipt" "$owned/frontend.log";done
 echo M10_ECOSYSTEM_SETTINGS_FRONTEND_PASS_checks=33_skipped=0
fi
if [[ "$mode" == browser || "$mode" == all ]];then bash scripts/verify_m10_ecosystem_settings_browser.sh;fi
printf 'M10_ECOSYSTEM_SETTINGS_FORMAL_PASS mode=%s\n' "$mode"
