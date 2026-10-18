#!/usr/bin/env bash
# Exercise the build gate using real Git; never stage the working repository.
set -euo pipefail
umask 077
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
work="$(mktemp -d /data/temp/pulse-build-provenance.XXXXXX)"
cleanup() {
  result=$?
  trap - EXIT
  case "$work" in /data/temp/pulse-build-provenance.*) test ! -L "$work"; rm -rf -- "$work" ;; *) exit 98 ;; esac
  test ! -e "$work" || exit 98
  printf 'OWNED_BUILD_PROVENANCE_TEMP_CLEANUP_VERIFIED\n'
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export TMPDIR="$work" TMP="$work" TEMP="$work"
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_COMMON_DIR GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES
fixture="$work/repo"
helper="$repo_root/scripts/build_revision.sh"
mkdir -p "$fixture" "$work/no-hooks"
git -c init.defaultBranch=main init -q "$fixture"
reject() {
  local observed
  if observed="$(bash "$helper" "$1" "$fixture" 2>"$work/rejection.log")"; then
    printf 'Expected build metadata rejection.\n' >&2
    exit 1
  fi
  test -z "$observed"
}
test "$(bash "$helper" dev "$fixture")" = uncommitted
reject 1.0.0-beta.1
printf 'source\n' > "$fixture/source.txt"
printf '*.cache\n' > "$fixture/.gitignore"
git -C "$fixture" add -- source.txt .gitignore
git -C "$fixture" -c core.hooksPath="$work/no-hooks" -c commit.gpgsign=false -c user.name='PulseMedia build test' -c user.email='build-test@invalid.example' commit -qm fixture
revision="$(git -C "$fixture" rev-parse --verify HEAD)"
test "$(bash "$helper" dev "$fixture")" = "$revision"
test "$(bash "$helper" 1.0.0-beta.1 "$fixture")" = "$revision"
test "$(GIT_DIR="$work/unrelated" GIT_INDEX_FILE="$work/unrelated-index" bash "$helper" 1.0.0-beta.1 "$fixture")" = "$revision"
printf 'changed\n' >> "$fixture/source.txt"
test "$(bash "$helper" dev "$fixture")" = "$revision-dirty"
reject 1.0.0-beta.1
git -C "$fixture" restore -- source.txt
printf 'untracked\n' > "$fixture/scratch.txt"
test "$(bash "$helper" dev "$fixture")" = "$revision-dirty"
reject 1.0.0-beta.1
rm -- "$fixture/scratch.txt"
printf 'generated\n' > "$fixture/output.cache"
test "$(bash "$helper" 1.0.0-beta.1 "$fixture")" = "$revision"
reject 'beta candidate'
printf 'BUILD_PROVENANCE_CHECK_PASSED no_head=true clean=true dirty_tracked=true untracked=true ignored_output=true alternate_index=true invalid_label=true\n'
