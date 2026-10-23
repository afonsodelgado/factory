---
name: coderabbit-comments
description: Review, triage, and address CodeRabbit review comments on GitHub pull requests. Use when the user asks to inspect unresolved CodeRabbit threads, decide which comments are relevant or stale, plan fixes, implement the valid ones, and re-check remaining review feedback on a PR.
---

# Coderabbit Comments

## Overview

Use this skill to turn CodeRabbit review feedback into a tight implementation loop with minimal blast radius. Treat every CodeRabbit comment as a hypothesis to verify against the current code, not as ground truth.

## Workflow

1. Identify the PR.
   - If the user gives a PR URL, use it directly.
   - If the user gives a PR number, use it with `--owner` and `--repo`.
   - If the user refers to "this branch" or "the current PR", use `gh pr view --json number,url` first.

2. Verify GitHub access before doing review work.
   - Use `gh auth status`.
   - If auth is missing or broken, tell the user to re-authenticate with `gh auth login`.
   - Run `gh` network commands with escalated permissions.

3. Fetch only the CodeRabbit threads that matter.
   - Prefer `scripts/fetch_coderabbit_threads.py`.
   - Default to unresolved, non-outdated review threads.
   - Ignore meta comments such as title scope, walkthrough automation, or docstring coverage unless the user explicitly wants them.

4. Validate each comment against the current code.
   - Read the exact file and nearby lines before accepting the comment.
   - Classify each item as:
     - relevant
     - partially relevant
     - stale or already fixed
     - not worth taking for this PR
   - Explain the reason briefly and concretely.

5. Always stop for confirmation before editing.
   - Before making any code changes, show:
     - the relevant comments
     - the irrelevant or stale comments
   - Give a brief explanation for each verdict.
   - Ask the user for explicit confirmation before proceeding with edits, even if they already asked to "address" the comments.

6. Report comments in a useful order.
   - Put findings first, ordered by severity.
   - Prioritize:
     - data loss
     - concurrency and race conditions
     - incorrect workflow state
     - broken retries
     - missing tests
     - prompt/docs cleanup
     - refactors and structure nits

7. Implement only the relevant fixes after approval.
   - Keep the blast radius small.
   - Prefer local fixes over refactors.
   - Do not "fix" cleanup-only comments unless the user asks for them.
   - Preserve behavior outside the commented path.
   - Do not commit or push any changes as part of this skill.
   - Leave the work as a local diff for user review unless the user later gives a separate explicit instruction to commit or push.

8. Validate after edits.
   - Run focused typechecks and tests for the touched code paths.
   - Re-fetch unresolved CodeRabbit comments after the patch set.
   - Tell the user which unresolved comments remain and whether they are still valid.

9. Do not resolve GitHub review threads unless the user explicitly asks.

## Factory mode

Applies when the `factory-shepherd` agent runs you, or the user says the ticket is running in the factory. Starting the factory is the user's confirmation and their authorisation to commit, push and reply, so steps 5, 7 and the Output Shape's confirmation requests do not apply. Instead:

1. Fetch and classify every unresolved, non-outdated thread exactly as in steps 3 and 4.
2. **Relevant and partially relevant:** fix, run the focused checks, commit. Reply on the thread after the push: one line naming the fix and the commit.
3. **Stale or already fixed:** reply with the commit or the file and line that already handles it.
4. **Not worth taking:** reply with the concrete reason in one or two sentences, for example: out of the approved scope, contradicts a repo rule, or the suggested change would break a named behaviour.
5. Never resolve threads. CodeRabbit resolves its own once it re-reviews, and the user sees the rest.
6. Write the triage table (thread, verdict, action, reply) to the file the caller names.

Reply to a thread:

```bash
gh api graphql -f threadId=<thread node id> -f body='<reply>' \
  -f query='mutation($threadId:ID!,$body:String!){ addPullRequestReviewThreadReply(input:{pullRequestReviewThreadId:$threadId, body:$body}){ comment{ url } } }'
```

The `--json` output of the fetch script carries each thread's node id.

## Command Patterns

Use the helper script:

```bash
python3 ~/.claude/skills/coderabbit-comments/scripts/fetch_coderabbit_threads.py <pr-url>
python3 ~/.claude/skills/coderabbit-comments/scripts/fetch_coderabbit_threads.py <pr-number> --owner SynthFlowAI --repo vector
python3 ~/.claude/skills/coderabbit-comments/scripts/fetch_coderabbit_threads.py <pr-url> --json
```

Fallback GraphQL shape when the script is not available:

```bash
gh api graphql -F owner=OWNER -F repo=REPO -F number=PR \
  -f query='query($owner:String!,$repo:String!,$number:Int!){ repository(owner:$owner,name:$repo){ pullRequest(number:$number){ reviewThreads(first:100){nodes{id isResolved isOutdated path line originalLine comments(first:100){nodes{id body createdAt updatedAt author{login}}}} } } } }'
```

## Review Heuristics

- Treat comments about data loss, invalid fallbacks, concurrency, and state machine correctness as high priority.
- Treat prompt wording, docs consistency, and cleanup refactors as lower priority unless they change tool behavior.
- When CodeRabbit suggests a fix, verify the code first. The suggested patch is often directionally right but not always complete.
- If a comment points to a test-only gap but implies a real production bug, fix the production code first, then add the regression test.

## Output Shape

When the user asks for comment triage, respond with:

1. A numbered list of CodeRabbit comments.
2. A relevance verdict for each comment.
3. A short fix plan for the relevant ones.
4. An explicit request for confirmation before any edits.

When the user asks to implement, first respond with:

1. The relevant comments.
2. The irrelevant or stale comments.
3. A brief explanation for each verdict.
4. A direct request for confirmation before any edits.

Only after the user confirms, proceed and then respond with:

1. The fixes you made.
2. The focused validations you ran.
3. Any unresolved comments that still look real.
4. Confirmation that nothing was committed or pushed.
