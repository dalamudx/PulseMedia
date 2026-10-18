#!/usr/bin/env bash
# Prove production dependency guards reject compiling forbidden imports.
set -euo pipefail
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
test_temp_root=${PULSE_TEST_TEMP_ROOT:-/data/temp}
mkdir -p "$test_temp_root"
test_temp_root=$(realpath "$test_temp_root")
owned=$(mktemp -d "$test_temp_root/pulse-depguard-probe.XXXXXX")
cleanup(){ rc=$?; trap - EXIT; case "$owned" in "$test_temp_root"/pulse-depguard-probe.*) rm -rf -- "$owned";; *) exit 1;; esac; test ! -e "$owned"; exit "$rc"; }
trap cleanup EXIT
export TMPDIR="$owned" TMP="$owned" TEMP="$owned" GOTMPDIR="$owned/build" GOLANGCI_LINT_CACHE="$owned/cache"
mkdir -p "$GOTMPDIR"
go_version=$(go env GOVERSION)
go_version=${go_version#go}
probe(){
 name=$1; target=$2; dependency=$3; symbol=$4; filename=${5:-probe.go}
 fixture="$owned/$name"
 mkdir -p "$fixture/$target"
 printf 'module github.com/dalamudx/PulseMedia\n\ngo %s\n' "$go_version" > "$fixture/go.mod"
 if [ "$dependency" = github.com/jackc/pgx/v5 ]; then
  cp "$repo/go.mod" "$repo/go.sum" "$fixture/"
 elif [ "$dependency" != net/http ]; then
  relative=${dependency#github.com/dalamudx/PulseMedia/}
  mkdir -p "$fixture/$relative"
  printf 'package dependency\nconst Value = 1\n' > "$fixture/$relative/stub.go"
 fi
 printf 'package probe\nimport forbidden "%s"\nvar _ = forbidden.%s\n' "$dependency" "$symbol" > "$fixture/$target/$filename"
 if (cd "$fixture" && golangci-lint run --config "$repo/.golangci.yml" --timeout=2m ./...) >"$owned/$name.log" 2>&1; then echo "Guard accepted $name" >&2; exit 1; fi
 if ! grep -F '(depguard)' "$owned/$name.log" >/dev/null; then cat "$owned/$name.log"; exit 1; fi
 echo "DEPENDENCY_REJECTED $name"
}
probe domain-http internal/domain/probe net/http MethodGet
probe domain-platform internal/domain/probe github.com/dalamudx/PulseMedia/internal/platform/probe Value
probe ports-usecase internal/ports github.com/dalamudx/PulseMedia/internal/usecase/probe Value
probe usecase-adapter internal/usecase/probe github.com/dalamudx/PulseMedia/internal/adapter/probe Value
probe httpapi-adapter internal/httpapi github.com/dalamudx/PulseMedia/internal/adapter/probe Value
probe library-store internal/usecase/library github.com/dalamudx/PulseMedia/internal/store/probe Value
probe library-driver internal/usecase/library github.com/jackc/pgx/v5 ErrNoRows
probe search-endpoint-store internal/usecase/search github.com/dalamudx/PulseMedia/internal/store/postgres Value endpoint.go
probe search-endpoint-driver internal/usecase/search github.com/jackc/pgx/v5 ErrNoRows endpoint.go
probe movie-matcher-store internal/usecase/media github.com/dalamudx/PulseMedia/internal/store/probe Value matcher.go
probe movie-matcher-driver internal/usecase/media github.com/jackc/pgx/v5 ErrNoRows matcher.go
probe mediaserver-store internal/usecase/mediaserver github.com/dalamudx/PulseMedia/internal/store/probe Value
probe mediaserver-driver internal/usecase/mediaserver github.com/jackc/pgx/v5 ErrNoRows
probe auth-store internal/usecase/auth github.com/dalamudx/PulseMedia/internal/store/probe Value
probe auth-platform internal/usecase/auth github.com/dalamudx/PulseMedia/internal/platform/probe Value
probe auth-driver internal/usecase/auth github.com/jackc/pgx/v5 ErrNoRows
probe notify-store internal/usecase/notify github.com/dalamudx/PulseMedia/internal/store/probe Value
probe notify-platform internal/usecase/notify github.com/dalamudx/PulseMedia/internal/platform/probe Value
probe notify-driver internal/usecase/notify github.com/jackc/pgx/v5 ErrNoRows
probe scheduler-store internal/usecase/scheduler github.com/dalamudx/PulseMedia/internal/store/probe Value
probe scheduler-platform internal/usecase/scheduler github.com/dalamudx/PulseMedia/internal/platform/probe Value
probe scheduler-driver internal/usecase/scheduler github.com/jackc/pgx/v5 ErrNoRows
probe mediaserver-platform internal/usecase/mediaserver github.com/dalamudx/PulseMedia/internal/platform/probe Value
probe subscription-store internal/usecase/subscription github.com/dalamudx/PulseMedia/internal/store/probe Value
probe subscription-platform internal/usecase/subscription github.com/dalamudx/PulseMedia/internal/platform/probe Value
probe subscription-driver internal/usecase/subscription github.com/jackc/pgx/v5 ErrNoRows
probe audit-store internal/usecase/audit github.com/dalamudx/PulseMedia/internal/store/probe Value
probe audit-platform internal/usecase/audit github.com/dalamudx/PulseMedia/internal/platform/probe Value
probe audit-driver internal/usecase/audit github.com/jackc/pgx/v5 ErrNoRows
probe site-store internal/usecase/site github.com/dalamudx/PulseMedia/internal/store/probe Value
probe site-platform internal/usecase/site github.com/dalamudx/PulseMedia/internal/platform/probe Value
probe site-driver internal/usecase/site github.com/jackc/pgx/v5 ErrNoRows
probe search-store internal/usecase/search github.com/dalamudx/PulseMedia/internal/store/probe Value
probe search-platform internal/usecase/search github.com/dalamudx/PulseMedia/internal/platform/probe Value
probe search-driver internal/usecase/search github.com/jackc/pgx/v5 ErrNoRows
probe usecase-peer internal/usecase/search github.com/dalamudx/PulseMedia/internal/usecase/probe Value
probe usecase-other-store internal/usecase/operation github.com/dalamudx/PulseMedia/internal/store/probe Value
probe usecase-other-platform internal/usecase/operation github.com/dalamudx/PulseMedia/internal/platform/probe Value
probe usecase-other-driver internal/usecase/operation github.com/jackc/pgx/v5 ErrNoRows
mkdir -p "$owned/valid/internal/domain/probe"
printf 'module github.com/dalamudx/PulseMedia\n\ngo %s\n' "$go_version" > "$owned/valid/go.mod"
printf 'package probe\nconst Value = 1\n' > "$owned/valid/internal/domain/probe/probe.go"
(cd "$owned/valid" && golangci-lint run --config "$repo/.golangci.yml" --timeout=2m ./...)
echo DEPENDENCY_GUARD_POSITIVE_AND_NEGATIVE_PASS
