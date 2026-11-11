#!/usr/bin/env bash
#
# Derives the next semantic version from the Conventional Commits made since
# the last release, and optionally writes release notes.
#
#   scripts/next-version.sh [--notes FILE]
#
# Prints key=value lines (the format of $GITHUB_OUTPUT):
#
#   version=0.3.0     next version; empty when nothing releasable was committed
#   minor=0.3         major.minor of it, for the floating image tag
#   previous=v0.2.4   the release it follows; empty for the first release
#
# The last release is the highest vX.Y.Z tag reachable from HEAD. Tags of any
# other shape (pre-releases, four-part versions, …) are ignored.
#
# Rules:
#   - no release yet               -> 0.1.0, whatever the commits say
#   - "feat"                       -> minor
#   - "fix", "perf", "revert"      -> patch
#   - "type!:" or a "BREAKING CHANGE:" footer
#                                  -> major (minor while the major is 0)
#   - anything else (docs, ci, chore, refactor, test, build, …) -> no release
#
# RELEASE_AS=1.0.0 in the environment forces that version instead. It must be
# higher than the last release.
#
# Any failure exits non-zero and prints no version, so a pipeline can never
# mistake an error for "nothing to release". Tested by next-version_test.sh.

set -euo pipefail

INITIAL_VERSION="0.1.0"
notes_file=""
while [ $# -gt 0 ]; do
  case "$1" in
    --notes) notes_file="${2:?--notes needs a file}"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

# Strict semantic version: three numbers without leading zeros.
semver='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'

# newer A B: is version A higher than version B?
newer() {
  local a1 a2 a3 b1 b2 b3
  IFS=. read -r a1 a2 a3 <<<"$1"
  IFS=. read -r b1 b2 b3 <<<"$2"
  [ "$a1" -ne "$b1" ] && { [ "$a1" -gt "$b1" ]; return; }
  [ "$a2" -ne "$b2" ] && { [ "$a2" -gt "$b2" ]; return; }
  [ "$a3" -gt "$b3" ]
}

release_as="${RELEASE_AS:-}"
release_as="${release_as#v}"
if [ -n "$release_as" ] && ! [[ "$release_as" =~ $semver ]]; then
  echo "RELEASE_AS must look like 1.2.3, got '$release_as'" >&2
  exit 2
fi

# The highest release tag in the history of HEAD. Not "the nearest tag": one
# commit can carry several (a forced 1.0.0 on top of 0.3.0), and the nearest
# one is picked by name, which would make the next release go backwards.
previous=""
while IFS= read -r tag; do
  v="${tag#v}"
  [[ "$v" =~ $semver ]] || continue
  if [ -z "$previous" ] || newer "$v" "${previous#v}"; then
    previous="$tag"
  fi
done < <(git tag --merged HEAD --list 'v*')

range="HEAD"
[ -n "$previous" ] && range="$previous..HEAD"

# ---- classify commits ----
subject_re='^([a-zA-Z]+)(\(([^)]+)\))?(!)?: (.+)$'
breaking_re=$'(^|\n)BREAKING[ -]CHANGE: ' # a footer: only at the start of a line
bump=0 # 0 none, 1 patch, 2 minor, 3 major
breaking="" features="" fixes="" perf="" other=""

US=$'\x1f' RS=$'\x1e'
while IFS= read -r -d "$RS" record; do
  record="${record#$'\n'}"
  [ -z "$record" ] && continue
  sha="${record%%"$US"*}"
  rest="${record#*"$US"}"
  subject="${rest%%"$US"*}"
  body="${rest#*"$US"}"

  type="" scope="" bang="" text="$subject"
  if [[ "$subject" =~ $subject_re ]]; then
    type="$(printf '%s' "${BASH_REMATCH[1]}" | tr '[:upper:]' '[:lower:]')"
    scope="${BASH_REMATCH[3]}"
    bang="${BASH_REMATCH[4]}"
    text="${BASH_REMATCH[5]}"
  fi
  line="- ${scope:+**$scope:** }$text ($sha)"

  level=0
  case "$type" in
    feat) level=2; features+="$line"$'\n' ;;
    fix | revert) level=1; fixes+="$line"$'\n' ;;
    perf) level=1; perf+="$line"$'\n' ;;
    *) other+="- $subject ($sha)"$'\n' ;;
  esac
  if [ -n "$bang" ] || [[ "$body" =~ $breaking_re ]]; then
    level=3
    breaking+="$line"$'\n'
  fi
  [ "$level" -gt "$bump" ] && bump="$level"
done < <(git log --no-merges --format="%h${US}%s${US}%b${RS}" "$range")

# ---- decide the version ----
version=""
if [ -n "$release_as" ]; then
  version="$release_as"
  if [ -n "$previous" ] && ! newer "$version" "${previous#v}"; then
    echo "RELEASE_AS=$version is not higher than the last release $previous" >&2
    exit 1
  fi
elif [ -z "$previous" ]; then
  version="$INITIAL_VERSION"
elif [ "$bump" -gt 0 ]; then
  IFS=. read -r major minor patch <<<"${previous#v}"
  if [ "$major" -eq 0 ] && [ "$bump" -eq 3 ]; then
    bump=2 # before 1.0 a breaking change is a minor release
  fi
  case "$bump" in
    3) major=$((major + 1)); minor=0; patch=0 ;;
    2) minor=$((minor + 1)); patch=0 ;;
    1) patch=$((patch + 1)) ;;
  esac
  version="$major.$minor.$patch"
fi

# A tag of that name outside this history (another branch) must not be reused.
if [ -n "$version" ] && git rev-parse -q --verify "refs/tags/v$version" >/dev/null; then
  echo "tag v$version already exists" >&2
  exit 1
fi

echo "version=$version"
echo "minor=${version%.*}"
echo "previous=$previous"

# ---- release notes ----
if [ -n "$notes_file" ] && [ -n "$version" ]; then
  {
    if [ -z "$previous" ]; then
      echo "First release of hoard."
      echo
    fi
    section() { # title, lines
      [ -z "$2" ] && return 0
      printf '## %s\n\n%s\n' "$1" "${2%$'\n'}"
    }
    section "Breaking changes" "$breaking"
    section "Features" "$features"
    section "Fixes" "$fixes"
    section "Performance" "$perf"
    # Housekeeping commits are only listed when a release has nothing else to show.
    if [ -n "$previous" ] && [ -z "$breaking$features$fixes$perf" ]; then
      section "Changes" "$other"
    fi
  } >"$notes_file"
fi
