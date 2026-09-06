#!/usr/bin/env bash
# Build, vet, format-check, then run every test as its own process, in parallel.
#
# WHY NOT `go test ./... -race`: that serialises each package's tests into one
# process, so the wall time is the sum of the slowest package's tests and a
# 40-core machine sits idle. Compiling each package's test binary ONCE and then
# fanning the individual test functions out across all cores turns the same
# work into N+K short processes that finish together.
#
# Exit codes are stamped into files rather than read through a pipe: a pipeline
# reports the LAST command's status, so `go test ... | tail` reports tail's,
# and a red run reads as green. That is the single most common way a check
# lies, so nothing here pipes a result.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
root="$PWD"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

jobs="${JOBS:-$( (nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 4) )}"
race=""
[ "${RACE:-0}" = "1" ] && race="-race"

fail=0
step() { printf '\n=== %s ===\n' "$1"; }

step "build"
if ! go build ./...; then fail=1; fi

step "vet"
if ! go vet ./...; then fail=1; fi

step "gofmt"
unformatted="$(gofmt -l . )"
if [ -n "$unformatted" ]; then
  echo "not gofmt-clean:"; echo "$unformatted"; fail=1
else
  echo "clean"
fi

# Compile one test binary per package that has tests. Doing this up front means
# the parallel run below is pure execution, with no compiler contention.
step "compiling test binaries"
pkgs="$(go list ./... )"
declare -a bins=()
for pkg in $pkgs; do
  name="${pkg##*/}"
  out="$work/$name.test"
  if go test $race -c -o "$out" "$pkg" 2>"$work/$name.build.log"; then
    if [ -f "$out" ]; then
      bins+=("$out")
      echo "  built $name"
    fi
  else
    echo "  FAILED to build tests for $pkg:"; cat "$work/$name.build.log"; fail=1
  fi
done

if [ ${#bins[@]} -eq 0 ]; then
  echo "no test binaries; nothing to run"
  exit $fail
fi

# Enumerate every top-level test function and emit one job line per function.
step "enumerating tests"
: >"$work/jobs"
total=0
for bin in "${bins[@]}"; do
  while read -r name; do
    case "$name" in Test*) printf '%s\t%s\n' "$bin" "$name" >>"$work/jobs"; total=$((total+1));; esac
  done < <("$bin" -test.list '.*' 2>/dev/null)
done
echo "  $total test functions across ${#bins[@]} packages, ${jobs} at a time${race:+ (race detector on)}"

step "running"
# One process per test function. Each writes its own status file, so no result
# is lost to interleaved output and the exit code of each is read back exactly.
run_one() {
  local bin="$1" name="$2" work="$3"
  local tag="$(basename "$bin" .test).$name"
  if "$bin" -test.run "^${name}\$" -test.count=1 >"$work/$tag.out" 2>&1; then
    echo "ok   $tag"
  else
    echo "FAIL $tag"
    printf '%s\n' "$tag" >>"$work/failed"
  fi
}
export -f run_one

: >"$work/failed"
# shellcheck disable=SC2016
awk -F'\t' -v w="$work" '{printf "%s\t%s\t%s\n", $1, $2, w}' "$work/jobs" \
  | xargs -P "$jobs" -I{} -d '\n' bash -c 'IFS=$'"'"'\t'"'"' read -r b n w <<< "{}"; run_one "$b" "$n" "$w"'

failed_count=$(wc -l <"$work/failed" | tr -d ' ')
if [ "$failed_count" -gt 0 ]; then
  fail=1
  step "failures ($failed_count)"
  while read -r tag; do
    printf '\n--- %s ---\n' "$tag"
    cat "$work/$tag.out"
  done <"$work/failed"
fi

step "result"
if [ "$fail" -eq 0 ]; then
  echo "PASS  $total tests, all green"
else
  echo "FAIL  $failed_count of $total tests failed (or an earlier gate did)"
fi
exit $fail
