# factory

`factory` runs every factory in one tmux session. A factory is a Claude Code session running `/factory` in its own worktree, identified by an id you choose and driven by a prompt you write: the prompt is the spec. The app lists factories on the left, shows the selected one's live session on the right, and puts a "Needs attention" panel under it for questions, gates and the rest.

## Install

Every merge to `main` publishes a release: the patch version goes up by one from the latest `v*` tag (the first is `v0.1.0`), with a binary for `darwin_arm64`, `darwin_amd64`, `linux_amd64` and `linux_arm64`. For a minor or major bump, push the tag yourself (`git tag v0.2.0 && git push origin v0.2.0`), and that release is published instead.

Download the binary for your machine from the private repo's latest release:

```sh
gh release download --repo afonsodelgado/factory --pattern 'factory_darwin_arm64' --output ~/.local/bin/factory
chmod +x ~/.local/bin/factory
```

Or build it with Go 1.26 or later. A binary you build has no release version: `factory version` shows `dev`, plus the commit and its date when you build from a checkout with `go build ./cmd/factory`. Built inside a git worktree, it shows the main checkout's commit, because Go reads the commit from the first `.git` directory above the source.

```sh
GOPRIVATE=github.com/afonsodelgado/* go install github.com/afonsodelgado/factory/cmd/factory@latest
```

Then check the machine and install the factory into your Claude config. `doctor` lists what is missing, each with the command that fixes it, and changes nothing; `bootstrap` installs the bundled skills, agents and hooks, backing up any file it replaces.

```sh
factory doctor
factory bootstrap
```

`bootstrap` never installs system packages; it prints the `doctor` commands for those. Running it again on an up-to-date machine changes nothing, and it keeps the hooks you already have in `settings.json`. Restart running Claude Code sessions afterwards so they pick up the hooks. `factory install-hooks --remove` takes the hooks out again.

A permission prompt marks its factory "needs you" until the approved tool has run, on the thread that asked: the `PostToolUse` and `PostToolUseFailure` hooks report that, and `doctor` lists them as missing until `bootstrap` or `install-hooks` adds them. A session started before you updated keeps its old hooks until you restart it, and a prompt that was already open when you updated shows as answered.

## Update

```sh
factory version    # what is running, and the latest release
factory --version  # only what is running, with no network call
factory update     # install the latest release, then run its bootstrap
```

`factory version` asks GitHub through `gh`. Without `gh`, or when `gh` is not logged in, it prints only the running version. `factory update` downloads the release binary for your machine next to the running one and replaces it. Then it runs the new binary's `bootstrap`, so the skills, agents and hooks match it. It changes nothing when you already run the latest release, and it refuses when it cannot write to the binary's directory. A running hub keeps the old version: quit it with `q` and run `factory` again.

## Add a repo

The factory works in any git repo. From anywhere inside it, or one of its worktrees:

```sh
factory init            # or: factory init ~/dev/some-repo
```

`init` writes the repo's factory profile, `.claude/factory.yml`, from the bundled template, with build, lint and test commands detected from `go.mod` or `package.json`. It adds the factory's files to the repo's `.git/info/exclude` and lists the repo in the config. Nothing is committed to the repo. Edit the profile to match the repo, and you are done; set `coderabbit: true` only if CodeRabbit reviews the repo. Running `init` again changes nothing. A running hub reads the config only when it starts, so quit it with `q` and run `factory` again.

Worktrees have no copy of the profile, so the skill and its agents read it from the main checkout. `profiles/cowork/` has cowork's profile.

## Use

| Command | Does |
|---|---|
| `factory open` | attaches to the hub, starting it if needed |
| `factory open csv-export --prompt "Export contacts as CSV for ops."` | selects the factory `csv-export`, or creates it with that prompt |
| `factory open csv-export Export contacts as CSV for ops.` | the same: the words after the id are the prompt |
| `factory open csv-export` | without a prompt, a new factory's supervisor asks for one |
| `factory open csv-export --repo ~/dev/factory` | in a given repo instead of the one holding the factory or the current directory |
| `factory open csv-export --pipeline quick` | creates it with the `quick` pipeline instead of the default one |
| `factory delete csv-export` | deletes the factory and everything local to it, so the id can be used again |

An id is made of lowercase letters, digits and dashes; `factory open "CSV Export"` uses `csv-export`. The command words (`open`, `init`, `doctor`, `bootstrap`, `install-hooks`, `version`, `update`, `hub`, `view`, `dialog`, `hook`, `help`, `delete`) are refused as ids by the `n` dialog and by `factory open`; a `/factory doctor` typed straight into Claude is not guarded. Ids match exactly, and an existing factory is selected, never duplicated. The factory reads an issue tracker only when the prompt asks it to, for example "read Linear issue ENG-1234".

The binary replaces the old `factory` shell function; remove that function from your shell config.

Inside the hub, `?` lists every key. `M-0` returns to the factory list from any pane of the hub; `q` quits the UI and leaves every factory running. `p` pauses the selected factory: Claude stops and its pane closes, while the worktree, branch and state stay, and `w` wakes it again. `a` archives a done, gone or exited factory and can remove its worktree; the factory directory then moves to `.claude/factory-archive/<id>` in the main checkout. It stays listed as archived history (`H` shows it), and a new factory with the same id starts fresh.

`d` deletes the selected factory in any status, and `factory delete <id> [--repo path]` does the same from the shell. Delete closes the factory's pane, force-removes its worktree, force-deletes its local branch and every local branch of its PR stack, and removes its factory and QA directories (in the worktree, the main checkout and `.claude/factory-archive`), its `.ready` marker, activity log, drafts and registry entry. A new factory with the same id then gets that id and starts empty. Pushed PRs and remote branches are never touched. Delete always asks first; when the worktree has uncommitted changes or a branch has commits no remote branch has, it lists them and asks a second time, and `factory delete` refuses that second question when stdin is not a terminal. `--yes` skips only the first question. A delete that fails part way says which step failed; running it again finishes the job.

## Configuration

`~/.config/factory/config.yml` (or `$FACTORY_CONFIG_DIR/config.yml`):

```yaml
repos: [~/dev/cowork, ~/dev/factory]  # added by factory init; empty by default
hub:
  session: factory
  return_key: M-0
stalled_after: 20m
alerts:
  bell: false
  macos: false
icons: nerd
claude:
  remote_control: true
```

`return_key: prefix` binds `prefix f` instead of `M-0`; `icons: unicode` draws plain Unicode instead of Nerd Font icons. Every other value above is the default.

State (registry, hook files, drafts, the hub socket) lives in `~/.local/state/factory` or `$FACTORY_STATE_DIR`. Each repo's main checkout carries its factory profile in `.claude/factory.yml`.

## Pipelines

A pipeline is the list of stages a factory runs. Two are built in, so they work with no config:

```yaml
pipelines:
  full:  [requirements, design, plan, implement, review, coderabbit, qa, qa_fixes, test_gaps, handoff]
  quick: [requirements, plan, implement, review, handoff]
default_pipeline: full
```

Put `pipelines` and `default_pipeline` in `config.yml` to add presets or replace a built-in by name. A repo's `.claude/factory.yml` can do the same, and it wins over `config.yml`. The keys are the stage keys above. `requirements` and `handoff` always run, even when a preset leaves them out. An unknown stage key is a config error: `factory doctor` reports it and `factory open` refuses to start, naming the file and the key.

Pick a pipeline when you create a factory: in the `n` dialog, `tab` cycles the presets on the prompt step, starting at `default_pipeline`. From the shell, use `factory open <id> --pipeline <name>`; an unknown name is refused with the list of available ones. The factory list shows the pipeline next to the stage when it is not the default.

A pipeline only removes stages. The gates stay: `quick` still publishes the plan and waits for your approval before any code is written. `coderabbit: false` in the repo profile still removes CodeRabbit from whichever pipeline you pick, and the design stage still runs only when the change has a user interface. Skipped stages show as `–` in the stage track.

