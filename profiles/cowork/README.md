# cowork factory profile

`factory.yml` here is cowork's factory profile. It reproduces how the factory ran in cowork before the profile existed: npm commands with `db:migrate`, the `vector-codebase` skill for the architect, web designs from `DESIGN.md` and the web tokens, browser QA on ports 3000 and 3001, and CodeRabbit on.

Browser QA follows cowork's own `.claude/skills/qa/SKILL.md` for the environment, seeding and port steps (`qa.notes`), so cowork's QA stays what it was. cowork's project `qa` and `qa-brief` skills keep working for a manual `/qa`; the factory uses the bundled `qa-factory` and `qa-brief-factory`.

## Install

The profile lives only in your local checkout; nothing is committed to cowork.

```sh
factory init ~/dev/cowork
cp ~/dev/factory/profiles/cowork/factory.yml ~/dev/cowork/.claude/factory.yml
```

`init` adds cowork to the factory config and keeps the factory's files out of git through `.git/info/exclude`. It writes a generic profile only when none exists, so copy this file over it afterwards (or before: `init` never overwrites a profile). Worktrees have no copy of the profile; the factory reads it from the main checkout, so every factory picks it up, including those on branches cut earlier.
