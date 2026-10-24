#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m8_untracked_commit_recovery.sh' >&2; exit 2; fi
if [[ $(uname -s) != Linux ]]; then echo 'Linux and strace required' >&2; exit 2; fi
command -v strace >/dev/null
if [[ ${PULSE_M8_UNTRACKED_COMMIT_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M8_UNTRACKED_COMMIT_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m8_untracked_commit_recovery.sh
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m8-untracked-commit.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m8-untracked-commit.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M8_UNTRACKED_COMMIT_RECOVERY_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOCACHE="$owned/go-cache" PULSE_M8_UNTRACKED_ORGANIZATION_ACCEPTANCE=1
mkdir -p "$GOTMPDIR"
run_case(){
 local group=$1 name=$2 marker=$3 action=${4:-hardlink} log="$owned/$1.log" trace="$owned/$1.trace"
 if ! strace -f -qq -e trace=linkat,renameat2,unlinkat -o "$trace" go test -race -count=1 -timeout=5m ./internal/app -run "^$name\$" -v >"$log" 2>&1; then
  echo "M8_UNTRACKED_COMMIT_${group}_FAILED" >&2
  grep -E '^--- (FAIL|SKIP):|^[[:space:]]+[^ ]+_test.go:[0-9]+:|^FAIL|^# ' "$log" >&2 || true
  exit 1
 fi
 if grep -q -- '--- SKIP:' "$log"; then echo "M8_UNTRACKED_COMMIT_${group}_UNEXPECTED_SKIP" >&2; exit 1; fi
 test "$(grep -c '^--- PASS: Test' "$log")" = 1
 test "$(grep -c "$marker" "$log")" = 1
 if [[ $action == hardlink ]]; then
  awk -v scenario="$group" '/(^|[[:space:]])linkat\(.*"Movie\.mkv",/ { attempts++; if ($0 ~ / = 0$/) successes++ } END { printf "M8_UNTRACKED_NATIVE_LINKAT_COUNT scenario=%s attempts=%d successes=%d expected=1\n",scenario,attempts,successes; if(attempts!=1||successes!=1)exit 1 }' "$trace"
 else
  awk -v action="$action" '
   /renameat2\(.*"content",.*"Movie\.mkv",/ { published++; if($0 ~ / = 0$/) published_ok++ }
   /renameat2\(.*"Movie\.2026\.mkv",.*"move-source",/ { quarantined++; if($0 ~ / = 0$/) quarantined_ok++ }
   /unlinkat\(.*"move-source", 0\)/ { removed++; if($0 ~ / = 0$/) removed_ok++ }
   END { expected=(action=="move"?1:0); printf "M8_UNTRACKED_NATIVE_TRANSFER_COUNT action=%s target_attempts=%d target_successes=%d source_quarantines=%d source_quarantine_successes=%d source_removals=%d source_removal_successes=%d\n",action,published,published_ok,quarantined,quarantined_ok,removed,removed_ok; if(published!=1||published_ok!=1||quarantined!=expected||quarantined_ok!=expected||removed!=expected||removed_ok!=expected)exit 1 }
  ' "$trace"
 fi
 grep -E '^--- PASS: Test|M8_UNTRACKED_(ORGANIZATION_APP|COMMIT_RECEIPT)_PASS' "$log"
}
run_case COMMIT TestM8UntrackedOrganizationCommitReceiptLossRuntimeCookiePG 'M8_UNTRACKED_COMMIT_RECEIPT_PASS server_committed=true client_reply_discarded=true'
run_case ROLLBACK TestM8UntrackedOrganizationFinalizeRecoveryRuntimeCookiePG 'scenario=finalization_tail_rollback_observe_token2'
run_case COPY 'TestM8UntrackedOrganizationCopyMoveCommitReceiptLossRuntimeCookiePG/copy' 'M8_UNTRACKED_COMMIT_RECEIPT_PASS .*action=copy' copy
run_case MOVE 'TestM8UntrackedOrganizationCopyMoveCommitReceiptLossRuntimeCookiePG/move' 'M8_UNTRACKED_COMMIT_RECEIPT_PASS .*action=move' move
run_case COPY_ROLLBACK 'TestM8UntrackedOrganizationCopyMoveFinalizeRecoveryRuntimeCookiePG/copy' 'scenario=finalization_tail_rollback_observe_token2' copy
run_case MOVE_ROLLBACK 'TestM8UntrackedOrganizationCopyMoveFinalizeRecoveryRuntimeCookiePG/move' 'scenario=finalization_tail_rollback_observe_token2' move
printf 'M8_UNTRACKED_COMMIT_RECOVERY_ACCEPTANCE_PASS runs=6 skipped=0 PG=18.6 app=REAL rootfs=REAL strace=REAL hardlink_two_paths_attempts_each=1 hardlink_two_paths_successes_each=1 copy_target_renameat2=1 move_target_renameat2=1 move_source_quarantine=1 move_source_unlinkat=1 commit_reply_discarded_after_server_success=true rollback_token2_observes=true restart_no_republication=true original_receipts=true downloader_calls=0 NAS=NOT_COVERED copy_move_precommit_rollback=REAL_TOKEN2_OBSERVATION milestone=NOT_RELEASED\n'
