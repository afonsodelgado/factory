#!/usr/bin/env bash
set -uo pipefail

dir=""
if [ "${1:-}" = "--dir" ]; then
  dir="${2:-}"
  shift 2
fi
inbox=""
if [ "${1:-}" = "--inbox" ]; then
  inbox="$dir/inbox"
  shift
fi

if [ "$#" -eq 0 ]; then
  echo "usage: wait-for-review.sh [--dir <factory dir> [--inbox]] <pr-number>..." >&2
  exit 64
fi

report=""

say() {
  echo "$1"
  report="$report$1
"
}

finish() {
  if [ -n "$inbox" ]; then
    mkdir -p "$inbox"
    jq -n --arg text "CI and CodeRabbit finished:
$report" '{text: $text}' > "$inbox/ci-$(date +%s).json"
  fi
  exit "$1"
}

poll_seconds="${FACTORY_POLL_SECONDS:-60}"
sleep_seconds="${FACTORY_SLEEP_SECONDS:-$poll_seconds}"
timeout_minutes="${FACTORY_TIMEOUT_MINUTES:-120}"
no_checks_minutes="${FACTORY_NO_CHECKS_MINUTES:-15}"

finished=" "
polls=0

is_finished() {
  case "$finished" in
    *" $1 "*) return 0 ;;
    *) return 1 ;;
  esac
}

heartbeat() {
  if [ -n "$dir" ]; then
    touch "$dir/.heartbeat"
  fi
}

pr_report() {
  local checks
  checks=$(gh pr checks "$1" --json name,bucket 2>/dev/null)
  [ -n "$checks" ] || checks="[]"
  jq -r '
    def status(sel):
      [.[] | select(sel)]
      | if length == 0 then "absent"
        elif any(.bucket == "pending") then "pending"
        elif any(.bucket == "fail" or .bucket == "cancel") then "fail"
        else "pass" end;
    "\(length) \(status(.name | test("coderabbit"; "i"))) \(status(.name | test("coderabbit"; "i") | not))"
  ' <<<"$checks"
}

open_threads() {
  local threads count
  threads=$(gh api graphql -F owner='{owner}' -F repo='{repo}' -F number="$1" -f query='
    query($owner: String!, $repo: String!, $number: Int!) {
      repository(owner: $owner, name: $repo) {
        pullRequest(number: $number) {
          reviewThreads(first: 100) {
            nodes { isResolved isOutdated comments(first: 100) { nodes { author { login } } } }
          }
        }
      }
    }' 2>/dev/null)
  count=$(jq -r '
    [.data.repository.pullRequest.reviewThreads.nodes[]
      | select((.isResolved or .isOutdated) | not)
      | select(any(.comments.nodes[]; .author.login == "coderabbitai"))]
    | length
  ' <<<"$threads" 2>/dev/null)
  echo "${count:-unknown}"
}

while :; do
  heartbeat
  elapsed_min=$(( polls * poll_seconds / 60 ))
  pending=""
  for pr in "$@"; do
    is_finished "$pr" && continue
    read -r total coderabbit ci <<<"$(pr_report "$pr")"
    if [ "$total" -eq 0 ] && [ "$elapsed_min" -ge "$no_checks_minutes" ]; then
      say "PR $pr: no-checks after ${elapsed_min}m"
      finished="$finished$pr "
      continue
    fi
    if [ "$coderabbit" != "pending" ] && [ "$coderabbit" != "absent" ] && [ "$ci" != "pending" ]; then
      say "PR $pr: coderabbit=$coderabbit ci=$ci threads=$(open_threads "$pr")"
      finished="$finished$pr "
      continue
    fi
    if [ "$coderabbit" = "absent" ] && [ "$ci" != "pending" ] && [ "$elapsed_min" -ge "$no_checks_minutes" ]; then
      say "PR $pr: coderabbit=absent ci=$ci after ${elapsed_min}m"
      finished="$finished$pr "
      continue
    fi
    pending="$pending $pr"
  done
  if [ -z "$pending" ]; then
    say "all done"
    finish 0
  fi
  if [ "$elapsed_min" -ge "$timeout_minutes" ]; then
    say "timeout after ${elapsed_min}m, still pending:$pending"
    finish 3
  fi
  sleep "$sleep_seconds"
  polls=$(( polls + 1 ))
done
