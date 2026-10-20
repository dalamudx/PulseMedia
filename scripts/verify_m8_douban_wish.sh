#!/usr/bin/env bash
set -euo pipefail
umask 077
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo"
if [[ $# != 0 ]]; then echo 'Usage: verify_m8_douban_wish.sh' >&2; exit 2; fi
if [[ ${PULSE_M8_DOUBAN_WISH_OWNED_PG:-} != 1 ]]; then
 exec env PULSE_M8_DOUBAN_WISH_OWNED_PG=1 bash scripts/with_podman_test_postgres.sh bash scripts/verify_m8_douban_wish.sh
fi
: "${PULSE_TEST_DATABASE_URL:?owned PostgreSQL required}"
owned=$(mktemp -d /data/temp/pulse-m8-douban-wish.XXXXXX)
cleanup(){
 rc=$?; trap - EXIT
 case "$owned" in /data/temp/pulse-m8-douban-wish.*) test ! -L "$owned"; rm -rf -- "$owned";; *) exit 98;; esac
 test ! -e "$owned" || exit 1
 echo M8_DOUBAN_WISH_TEMP_CLEANUP_VERIFIED
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build"
mkdir -p "$GOTMPDIR"
go test -race -count=1 -timeout=5m ./internal/app -run '^TestM8DoubanWish' -v 2>&1 | tee "$owned/acceptance.log"
if grep -q -- '--- SKIP:' "$owned/acceptance.log"; then echo M8_DOUBAN_WISH_UNEXPECTED_SKIP >&2; exit 1; fi
test "$(grep -c '^--- PASS: TestM8DoubanWish' "$owned/acceptance.log")" = 3
for name in PositivePG FailurePG DisableLatePG; do
 test "$(grep -c "^--- PASS: TestM8DoubanWish$name (" "$owned/acceptance.log")" = 1
done
printf 'M8_DOUBAN_WISH_ACCEPTANCE_PASS parents=3 skipped=0 inventory=SQL_PREPARED_ACCOUNT_ONLY roots=NATIVE_SCAN network=OWNED_NATIVE_FIXTURES Add=1 hardlink=true ingestion=IMPORTED\n'
