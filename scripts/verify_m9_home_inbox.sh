#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
mode=${1:-all}
case "$mode" in backend|browser|all) ;; *) echo 'usage: verify_m9_home_inbox.sh [backend|browser|all]' >&2; exit 2 ;; esac
owned=$(mktemp -d /data/temp/pulse-m9-home-inbox.XXXXXX)
cleanup() { local rc=$?; trap - EXIT; rm -rf -- "$owned"; test ! -e "$owned" || exit 1; echo M9_HOME_INBOX_FORMAL_TEMP_CLEANUP_VERIFIED; exit "$rc"; }
trap cleanup EXIT
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build"
mkdir -p "$GOTMPDIR"
if [[ "$mode" != browser ]]; then
 export PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE=1 PULSE_M9_INBOX_GROUP_WORK="$owned"
 bash scripts/with_podman_test_postgres.sh bash -c '
  set -euo pipefail
  for group in SERVICE STORE PUBLIC; do
   case "$group" in
    SERVICE) pkg=./internal/usecase/subscription; expected=3; pattern="^TestMediaRequestInboxService" ;;
    STORE) pkg=./internal/store/postgres; expected=3; pattern="^TestMediaRequestInbox" ;;
    PUBLIC) pkg=./internal/httpapi; expected=2; pattern="^TestM9MediaRequestInbox" ;;
   esac
   log="$PULSE_M9_INBOX_GROUP_WORK/$group.log"
   if ! go test -race "$pkg" -run "$pattern" -count=1 -v -timeout=4m >"$log" 2>&1; then tail -n 100 "$log"; exit 1; fi
   count=$(grep -c "^--- PASS: Test" "$log" || true)
   if [[ "$count" != "$expected" ]] || grep -q -- "--- SKIP:" "$log"; then tail -n 100 "$log"; echo "unexpected $group count=$count" >&2; exit 1; fi
   grep -E "^--- PASS:|M9_INBOX" "$log" || true
  done
 '
 node --test --test-reporter=tap web/tests/pending-requests.test.mjs >"$owned/frontend.log" 2>&1 || { tail -n 100 "$owned/frontend.log"; exit 1; }
 for receipt in '# tests 11' '# pass 11' '# fail 0' '# skipped 0'; do grep -Fxq "$receipt" "$owned/frontend.log" || { tail -n 100 "$owned/frontend.log"; exit 1; }; done
 echo M9_HOME_INBOX_BACKEND_PASS_parents=8_skipped=0_frontend=11
fi
if [[ "$mode" != backend ]]; then bash scripts/verify_m9_home_inbox_ui.sh; fi
echo M9_HOME_INBOX_FORMAL_ACCEPTANCE_PASS_mode="$mode"
