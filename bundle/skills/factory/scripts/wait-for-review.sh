#!/usr/bin/env bash
set -uo pipefail

if [ "$#" -eq 0 ]; then
  echo "usage: wait-for-review.sh <pr-number>..." >&2
  exit 64
fi

poll_seconds="${FACTORY_POLL_SECONDS:-60}"
timeout_minutes="${FACTORY_TIMEOUT_MINUTES:-120}"
no_checks_minutes="${FACTORY_NO_CHECKS_MINUTES:-15}"

finished=" "
started=$(date +%s)

is_finished() {
  case "$finished" in
    *" $1 "*) return 0 ;;
    *) return 1 ;;
  esac
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

while :; do
  elapsed_min=$(( ( $(date +%s) - started ) / 60 ))
  pending=""
  for pr in "$@"; do
    is_finished "$pr" && continue
    read -r total coderabbit ci <<<"$(pr_report "$pr")"
    if [ "$total" -eq 0 ] && [ "$elapsed_min" -ge "$no_checks_minutes" ]; then
      echo "PR $pr: no-checks after ${elapsed_min}m"
      finished="$finished$pr "
      continue
    fi
    if [ "$coderabbit" != "pending" ] && [ "$coderabbit" != "absent" ] && [ "$ci" != "pending" ]; then
      echo "PR $pr: coderabbit=$coderabbit ci=$ci"
      finished="$finished$pr "
      continue
    fi
    if [ "$coderabbit" = "absent" ] && [ "$ci" != "pending" ] && [ "$elapsed_min" -ge "$no_checks_minutes" ]; then
      echo "PR $pr: coderabbit=absent ci=$ci after ${elapsed_min}m"
      finished="$finished$pr "
      continue
    fi
    pending="$pending $pr"
  done
  if [ -z "$pending" ]; then
    echo "all done"
    exit 0
  fi
  if [ "$elapsed_min" -ge "$timeout_minutes" ]; then
    echo "timeout after ${elapsed_min}m, still pending:$pending"
    exit 3
  fi
  sleep "$poll_seconds"
done
