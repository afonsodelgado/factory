# cowork factory profile

`factory.yml` here is cowork's factory profile. It reproduces how the factory ran in cowork before the profile existed: npm commands with `db:migrate`, the `vector-codebase` skill for the architect, web designs from `DESIGN.md` and the web tokens, browser QA on ports 3000 and 3001, and CodeRabbit on.

Browser QA follows cowork's own `.claude/skills/qa/SKILL.md` for the environment, seeding and port steps (`qa.notes`), so cowork's QA stays what it was. cowork's project `qa` and `qa-brief` skills keep working for a manual `/qa`; the factory uses the bundled `qa-factory` and `qa-brief-factory`.

## Install

From the cowork repo:

```sh
cp ~/dev/factory/profiles/cowork/factory.yml .claude/factory.yml
git add .claude/factory.yml
git commit -m "chore: add the factory profile"
```

Nothing else in cowork changes. The file is tracked, so every new worktree has it; factories on branches cut before the commit need it merged or copied in.
