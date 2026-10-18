#!/usr/bin/env bash
# Complete one large package in disjoint groups; each group owns a private PG18.
set -euo pipefail
umask 077
package="${1:?Usage: beta_grouped_race.sh ./internal/PACKAGE [groups]}"
groups="${2:-8}"
case "$package" in ./internal/httpapi|./internal/store/postgres) ;; *) printf 'Unsupported grouped package\n' >&2; exit 2 ;; esac
[[ "$groups" =~ ^[1-9][0-9]?$ ]] || exit 2
work=$(mktemp -d /data/temp/pulse-beta-grouped.XXXXXX)
cleanup() {
  result=$?
  trap - EXIT
  case "$work" in /data/temp/pulse-beta-grouped.*) test ! -L "$work"; rm -rf -- "$work" ;; *) exit 98 ;; esac
  test ! -e "$work" || exit 98
  printf 'BETA_GROUPED_TEMP_CLEANUP_VERIFIED package=%s\n' "$package"
  exit "$result"
}
trap cleanup EXIT
export TMPDIR="$work" TMP="$work" TEMP="$work"
go test -race -c -o "$work/tests" "$package"
mapfile -t names < <("$work/tests" -test.list '^Test' | awk '/^Test[A-Za-z0-9_]+$/')
((${#names[@]} > 0)) || exit 1
printf 'BETA_GROUPED_INVENTORY package=%s top_level_tests=%s groups=%s race=true\n' "$package" "${#names[@]}" "$groups"
failed=0
covered=0
for ((group=0; group<groups; group++)); do
  expression='^('
  count=0
  for ((i=group; i<${#names[@]}; i+=groups)); do
    ((count == 0)) || expression+='|'
    expression+="${names[i]}"
    count=$((count+1))
  done
  ((count > 0)) || continue
  expression+=')$'
  covered=$((covered+count))
  printf 'BETA_GROUP_BEGIN package=%s group=%s selected=%s\n' "$package" "$((group+1))" "$count"
  if bash scripts/with_podman_test_postgres.sh bash -c 'cd -- "$1"; shift; exec "$@"' bash "$package" "$work/tests" -test.v -test.run "$expression" -test.timeout=12m 2>&1 | awk '
    {last[NR%80]=$0}
    /^--- PASS:/ {passed++}
    /^--- SKIP:/ {skipped++}
    /^--- FAIL:/ {bad=1; print}
    /WARNING: DATA RACE|panic:|^FAIL$|Owned test databases were not confirmed clean/ {bad=1; print}
    /OWNED_PODMAN_TEST/ {print}
    END {
      printf "BETA_GROUP_RESULT passed=%d skipped=%d failures=%d\n", passed,skipped,bad
      if (bad) {start=NR>80?NR-79:1; for (n=start;n<=NR;n++) print last[n%80]}
    }'; then
    printf 'BETA_GROUP_PASS package=%s group=%s\n' "$package" "$((group+1))"
  else
    failed=$((failed+1))
    printf 'BETA_GROUP_FAIL package=%s group=%s\n' "$package" "$((group+1))"
  fi
done
((covered == ${#names[@]})) || exit 1
printf 'BETA_GROUPED_COMPLETE package=%s covered=%s failed_groups=%s\n' "$package" "$covered" "$failed"
((failed == 0))
