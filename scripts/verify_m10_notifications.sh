#!/usr/bin/env bash
set -euo pipefail
mode=${1:-all}
case "$mode" in backend|browser|all);;*) echo 'usage: verify_m10_notifications.sh [backend|browser|all]' >&2;exit 2;;esac
work=$(mktemp -d /data/temp/pulse-m10-notifications.XXXXXX)
cleanup(){ local rc=$?;rm -rf -- "$work";test ! -e "$work" || exit 1;echo M10_NOTIFICATIONS_FORMAL_CLEANUP_VERIFIED;exit "$rc"; }
trap cleanup EXIT
export TMPDIR="$work" TMP="$work" TEMP="$work" PULSE_M10_NOTICE_FORMAL_WORK="$work"
run_group(){
 local label=$1 package=$2 expected=$3 pattern=$4 count
 local log="$work/$label.log"
 if ! go test -race "$package" -count=1 -timeout=10m -run "$pattern" -v >"$log" 2>&1;then tail -n 130 "$log";return 1;fi
 count=$(grep -c '^--- PASS: Test' "$log" || true)
 if [[ "$count" != "$expected" ]] || grep -q -- '--- SKIP:' "$log";then tail -n 100 "$log";printf 'bad %s count=%s expected=%s\n' "$label" "$count" "$expected" >&2;return 1;fi
 grep -E '^--- PASS:|M10_NOTIFICATION' "$log" || true
 printf 'M10_NOTIFICATIONS_GROUP_PASS label=%s parents=%s skipped=0\n' "$label" "$count"
}
export -f run_group
if [[ "$mode" != browser ]];then
 mise exec -- node --test --test-reporter=tap web/tests/notification-test-command.test.mjs >"$work/frontend.log" 2>&1 || { tail -n 70 "$work/frontend.log";exit 1; }
 for receipt in '# tests 6' '# pass 6' '# fail 0' '# skipped 0' '# cancelled 0';do grep -Fxq "$receipt" "$work/frontend.log";done
 mise exec -- task test-web
 mise exec -- bash -c 'work="$PULSE_M10_NOTICE_FORMAL_WORK";run_group DOMAIN ./internal/domain/notification 3 "^Test(RobotDestinations|ChannelKinds|ChannelArtwork)$";run_group ROBOT ./internal/adapter/notify/im 8 "^TestRobot";run_group TELEGRAM ./internal/adapter/notify/telegram 1 "^TestTelegram";run_group WEBHOOK ./internal/adapter/notify/webhook 2 "^TestWebhook"'
 mise exec -- bash scripts/with_podman_test_postgres.sh bash -c '
  set -euo pipefail
  work="$PULSE_M10_NOTICE_FORMAL_WORK"
  run_group USECASE ./internal/usecase/notify 7 "^Test"
  run_group STORE ./internal/store/postgres 3 "^Test(ChannelStateAtomicFenceAndDisabledBudget|NotificationRobotSchema116HistoryPG|OutboxMigrationQuarantinesPlaintextAndUnspecifiedRecipients)$"
  run_group HTTP ./internal/httpapi 6 "^Test(NotificationConfigurationAdminEncryptedAndNeverReturned|ChannelStateHTTPRecovery|ChannelReplacementAtomicSafeReplayAndHistory|NotificationBatchHTTPTruthAndPrivacy|M10Notification(CookieConfigurationAndOriginal|TestTailRollback)PG)$"
  run_group OUTBOX ./internal/platform/outbox 2 "^Test(NotificationBatchSnapshotRollbackSealAndBound|OutboxConsumerIsolationAndOrdering)$"
  run_group APP ./internal/app 16 "^Test(ChannelReplacementAppRunKeepsOldPhysicalLeaseAndOnlyFutureRecipient|NotificationDisabledDuringFactoryNeverSends|ChannelStateAppRunResumeAndInFlightOwnership|M10NotificationNativeAppRuntimePG|NotificationBatchAppRunRestoresOpenWindow|NotificationCreateReceiptFailureRollsBackAndRecovers|NotificationManagementCurrentAccountAndReceiptProtection|NotificationStateRejectsUserChangeAfterLockWait|AppRunOwnsOrganizationAndOutbox|OutboxConfigurationIdempotencyInsertFailureRollsBack|ImportedOutboxRuntimeIndependentRecipients|OutboxCrashAfterSendReplaysStableEventID|OutboxSendRejectsWrongAuthorityAndRecipient|OutboxCommitChecksClockAfterRowLockWait|ImportedOutboxFanoutFailureRollsBackAllBusinessWrites|OutboxConcurrentAggregatePublication)$"
 '
 echo M10_NOTIFICATIONS_BACKEND_FORMAL_PASS_parents=48_frontend=6_skipped=0
fi
if [[ "$mode" != backend ]];then mise exec -- bash scripts/verify_m10_notification_browser.sh;fi
printf 'M10_NOTIFICATIONS_FORMAL_PASS mode=%s\n' "$mode"
