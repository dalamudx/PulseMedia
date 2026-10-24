#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-all}
case "$mode" in all|backend|frontend|browser);;*)echo 'expected all, backend, frontend or browser' >&2;exit 2;;esac
work=$(mktemp -d /data/temp/pulse-m10-bangumi-discovery.XXXXXX)
cleanup(){ rc=$?;rm -rf -- "$work";test ! -e "$work" || exit 1;echo M10_BANGUMI_DISCOVERY_TEMP_CLEANUP_VERIFIED;exit "$rc"; }
trap cleanup EXIT
export TMPDIR="$work" TMP="$work" TEMP="$work"
if [[ "$mode" == all || "$mode" == frontend ]];then
 mise exec -- task test-web
 mise exec -- node --test --test-reporter=tap web/tests/bangumi-calendar.test.mjs web/tests/discovery-catalog.test.mjs >"$work/frontend.log" 2>&1 || { tail -n 80 "$work/frontend.log";exit 1; }
 for receipt in '# tests 20' '# pass 20' '# fail 0' '# skipped 0' '# cancelled 0';do grep -Fxq "$receipt" "$work/frontend.log";done
 echo M10_BANGUMI_DISCOVERY_FRONTEND_PASS_checks=20_skipped=0
fi
if [[ "$mode" == all || "$mode" == backend ]];then mise exec -- bash scripts/verify_m10_bangumi_calendar.sh;fi
if [[ "$mode" == all || "$mode" == browser ]];then mise exec -- bash scripts/verify_m10_bangumi_browser.sh;fi
printf 'M10_BANGUMI_DISCOVERY_FORMAL_PASS mode=%s\n' "$mode"
