#!/usr/bin/env bash
# Resolve truthful build metadata without changing the source repository.
set -euo pipefail
version="${1:-dev}"
source_root="${2:?explicit source directory required}"
if [[ ! "$version" =~ ^[A-Za-z0-9][A-Za-z0-9.+_-]*$ ]]; then
  printf 'Build version must be a nonempty portable label.\n' >&2
  exit 2
fi
# An inherited alternate index/worktree must not identify unrelated source.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_COMMON_DIR GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES
if ! revision="$(git -C "$source_root" rev-parse --verify HEAD 2>/dev/null)"; then
  if [[ "$version" == dev ]]; then
    printf 'uncommitted\n'
    exit 0
  fi
  printf 'A non-development build requires committed source.\n' >&2
  exit 1
fi
if ! source_state="$(git -c core.fsmonitor=false -C "$source_root" status --porcelain=v1 --untracked-files=normal 2>/dev/null)"; then
  printf 'Cannot verify source state for build metadata.\n' >&2
  exit 1
fi
if [[ -n "$source_state" ]]; then
  if [[ "$version" != dev ]]; then
    printf 'A non-development build requires a clean source tree.\n' >&2
    exit 1
  fi
  printf '%s-dirty\n' "$revision"
  exit 0
fi
printf '%s\n' "$revision"
