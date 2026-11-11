#!/usr/bin/env bash
#
# Tests for next-version.sh, which decides what gets released. Each case
# builds a throwaway git history and checks the computed version.
#
#   scripts/next-version_test.sh

set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/next-version.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
failures=0
n=0

repo() { # start a fresh repository
  n=$((n + 1))
  dir="$work/$n"
  mkdir -p "$dir"
  cd "$dir"
  git init -q -b main .
  git config user.email test@example.invalid
  git config user.name test
  git config commit.gpgsign false
  git config tag.gpgsign false
}
c() { git commit -q --allow-empty -m "$1" ${2:+-m "$2"}; } # subject [body]

# expect NAME WANT: WANT is the version, "" for no release, or "error".
expect() {
  local name="$1" want="$2" out got
  if out="$("$script" 2>/dev/null)"; then
    got="$(printf '%s\n' "$out" | sed -n 's/^version=//p')"
  else
    got="error"
  fi
  if [ "$got" = "$want" ]; then
    printf 'ok    %s\n' "$name"
  else
    printf 'FAIL  %s: got "%s", want "%s"\n' "$name" "$got" "$want"
    failures=$((failures + 1))
  fi
}

repo; c "Add everything"; c "another plain commit"
expect "no release yet is 0.1.0 whatever the commits say" "0.1.0"

git tag v0.1.0
expect "nothing since the last release" ""
c "docs: guide"; c "ci: scanning"; c "chore(deps-dev): bump vite"; c "refactor: tidy"
expect "housekeeping types release nothing" ""
c "fix(mcp): reject empty ids (#12)"
expect "fix is a patch" "0.1.1"
c "feat: tag manager (#13)"
expect "feat is a minor" "0.2.0"
c "feat(api)!: rename a field"
expect "breaking before 1.0 is a minor" "0.2.0"

repo; c "init"; git tag v0.2.0; c "perf: faster thumbnails"
expect "perf is a patch" "0.2.1"
c "revert: undo something"
expect "revert is a patch" "0.2.1"
c "refactor: move config" "Details.

BREAKING CHANGE: the config file moved"
expect "breaking footer before 1.0 is a minor" "0.3.0"

repo; c "init"; git tag v1.4.2; c "docs: mention it" "This text mentions BREAKING CHANGE: in the middle of a line."
expect "a breaking marker mid-line is not a footer" ""
c "FIX: upper-case type"
expect "types are case-insensitive" "1.4.3"
c "feat!: drop the old API"
expect "breaking after 1.0 is a major" "2.0.0"

repo; c "init"; git tag v0.9.0; c "fix: a"; git tag v0.10.0; c "fix: b"
expect "0.10.0 is higher than 0.9.0" "0.10.1"

# A forced release on an already released commit: two tags on one commit.
repo; c "init"; c "feat: x"; git tag v0.3.0; git tag v1.0.0; c "fix: y"
expect "the highest tag on a commit wins, not the first by name" "1.0.1"
git tag -a -m annotated v1.1.0 HEAD~1
expect "annotated and lightweight tags are treated alike" "1.1.1"

repo; c "init"; git tag v1.0.0; c "fix: z"
git tag v1.0.1-rc.1; git tag v1.2.3.4; git tag v01.08.0; git tag v2026.11.11-nightly; git tag vnext
expect "tags that are not plain releases are ignored" "1.0.1"

repo; c "init"; git tag v0.5.0; c "docs: nothing releasable"
RELEASE_AS=1.0.0 expect "release_as forces a version" "1.0.0"
RELEASE_AS=v1.0.0 expect "release_as accepts a leading v" "1.0.0"
RELEASE_AS=0.5.0 expect "release_as equal to the last release is refused" "error"
RELEASE_AS=0.4.9 expect "release_as lower than the last release is refused" "error"
RELEASE_AS=1.0 expect "release_as must have three parts" "error"
RELEASE_AS=01.2.3 expect "release_as must not have leading zeros" "error"
RELEASE_AS='1.0.0; echo pwned' expect "release_as is not evaluated" "error"

# A tag of the computed name on another branch must not be reused.
repo; c "init"; git tag v0.1.0; git checkout -q -b side; c "fix: elsewhere"; git tag v0.1.1
git checkout -q main; c "fix: here"
expect "an existing tag elsewhere is an error, not a silent reuse" "error"

# Merge commits are skipped; the commits they bring in count.
repo; c "init"; git tag v0.1.0; git checkout -q -b topic; c "feat: on a branch"
git checkout -q main; c "docs: on main"; git merge -q --no-ff -m "Merge topic" topic
expect "commits from a merged branch count" "0.2.0"

repo; c "init"; git tag v0.1.0
c 'fix: `rm -rf /` $(touch pwned) ; echo'
expect "hostile subjects are data" "0.1.1"
[ -e pwned ] && { echo "FAIL  a commit subject was executed"; failures=$((failures + 1)); }

# Notes.
repo; c "init"; git tag v0.1.0; c "feat(ui): tag manager"; c "fix: crash on empty search"; c "docs: guide"
"$script" --notes "$work/notes.md" >/dev/null
if grep -q '^## Features' "$work/notes.md" && grep -q '\*\*ui:\*\* tag manager' "$work/notes.md" &&
  grep -q '^## Fixes' "$work/notes.md" && ! grep -q 'guide' "$work/notes.md"; then
  echo "ok    notes list features and fixes, not housekeeping"
else
  echo "FAIL  notes content"; cat "$work/notes.md"; failures=$((failures + 1))
fi

if [ "$failures" -gt 0 ]; then
  echo "$failures failed"
  exit 1
fi
echo "all passed (bash ${BASH_VERSION})"
