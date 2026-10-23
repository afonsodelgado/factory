#!/usr/bin/env python3
"""Fetch unresolved, non-outdated CodeRabbit review threads for a GitHub PR."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
from typing import Any

QUERY = """
query($owner:String!,$repo:String!,$number:Int!){
  repository(owner:$owner,name:$repo){
    pullRequest(number:$number){
      number
      url
      title
      reviewThreads(first:100){
        nodes{
          id
          isResolved
          isOutdated
          path
          line
          originalLine
          comments(first:100){
            nodes{
              id
              body
              createdAt
              updatedAt
              author{login}
            }
          }
        }
      }
    }
  }
}
""".strip()


def parse_target(target: str, owner: str | None, repo: str | None) -> tuple[str, str, int]:
    match = re.match(r"https://github\.com/([^/]+)/([^/]+)/pull/(\d+)", target)
    if match:
        return match.group(1), match.group(2), int(match.group(3))
    if target.isdigit() and owner and repo:
        return owner, repo, int(target)
    raise SystemExit(
        "Pass a PR URL or a PR number with --owner and --repo.",
    )


def run_gh_graphql(owner: str, repo: str, number: int) -> dict[str, Any]:
    cmd = [
        "gh",
        "api",
        "graphql",
        "-F",
        f"owner={owner}",
        "-F",
        f"repo={repo}",
        "-F",
        f"number={number}",
        "-f",
        f"query={QUERY}",
    ]
    try:
        completed = subprocess.run(
            cmd,
            check=True,
            text=True,
            capture_output=True,
        )
    except subprocess.CalledProcessError as exc:
        message = exc.stderr.strip() or exc.stdout.strip() or str(exc)
        raise SystemExit(f"gh api graphql failed: {message}") from exc
    return json.loads(completed.stdout)


def collect_threads(payload: dict[str, Any]) -> dict[str, Any]:
    pr = payload["data"]["repository"]["pullRequest"]
    rows: list[dict[str, Any]] = []
    for thread in pr["reviewThreads"]["nodes"]:
        if thread["isResolved"] or thread["isOutdated"]:
            continue
        comments = [
            {
                "author": comment["author"]["login"] if comment["author"] else None,
                "body": comment["body"],
            }
            for comment in thread["comments"]["nodes"]
        ]
        if not any(comment["author"] == "coderabbitai" for comment in comments):
            continue
        rows.append(
            {
                "id": thread["id"],
                "path": thread["path"],
                "line": thread["line"],
                "originalLine": thread["originalLine"],
                "comments": comments,
            }
        )
    return {
        "number": pr["number"],
        "url": pr["url"],
        "title": pr["title"],
        "threads": rows,
    }


def print_summary(data: dict[str, Any]) -> None:
    print(f"PR #{data['number']}: {data['title']}")
    print(data["url"])
    print()
    for index, thread in enumerate(data["threads"], start=1):
        location = thread["path"]
        if thread["line"] is not None:
            location += f":{thread['line']}"
        print(f"{index}. {location}")
        for comment in thread["comments"]:
            author = comment["author"] or "unknown"
            body = comment["body"].strip().replace("\r\n", "\n")
            print(f"   [{author}] {body}")
        print()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("target", help="PR URL, or PR number with --owner/--repo")
    parser.add_argument("--owner")
    parser.add_argument("--repo")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()

    owner, repo, number = parse_target(args.target, args.owner, args.repo)
    payload = run_gh_graphql(owner, repo, number)
    data = collect_threads(payload)

    if args.json:
        print(json.dumps(data, indent=2))
    else:
        print_summary(data)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
