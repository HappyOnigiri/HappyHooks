# Happy Hooks

English | [日本語](README.ja.md)

Happy Hooks helps Claude Code and Codex stay on track during long-running tasks. Its hooks come in a single Go binary called `hhx`.

## Features

- **Keep work moving:** Guard against avoidable confirmation prompts and common mistakes that interrupt an agent.
- **Wait efficiently:** Give the agent a way to wait for CI without repeatedly checking individual jobs.
- **Add context when needed:** Supply relevant PR details and local instructions as the agent works.

Command guards inspect text statically. They catch common mistakes, but are not a security boundary.

## Hooks

| Hook | What it does | Default |
| --- | --- | --- |
| [`pr-merge-guard`](docs/hooks/pr-merge-guard.md) | Stops agents from merging PRs | On |
| [`discard-guard`](docs/hooks/discard-guard.md) | Saves a snapshot before Git commands discard changes | On |
| [`idle-wait-guard`](docs/hooks/idle-wait-guard.md) | Stops commands used only to fill time | On |
| [`forbidden-term-guard`](docs/hooks/forbidden-term-guard.md) | Blocks configured terms in PR and issue bodies | On |
| [`irreversible-guard`](docs/hooks/irreversible-guard.md) | Blocks irreversible operations | On |
| [`generated-edit-guard`](docs/hooks/generated-edit-guard.md) | Stops hand edits of generated files | On |
| [`dangerous-rm-guard`](docs/hooks/dangerous-rm-guard.md) | Stops risky `rm` commands before Claude Code asks for confirmation | On (Claude Code) |
| [`exit-plan-subagent-guard`](docs/hooks/exit-plan-subagent-guard.md) | Keeps plan mode open until background agents finish | On (Claude Code) |
| [`git-hookspath-guard`](docs/hooks/git-hookspath-guard.md) | Blocks changes to Git hook settings | On |
| [`pr-context`](docs/hooks/pr-context.md) | Adds context about PR links in prompts | On |
| [`push-ci-context`](docs/hooks/push-ci-context.md) | Explains how to wait for CI after a push | On |
| [`pr-body-staleness`](docs/hooks/pr-body-staleness.md) | Flags PR descriptions that may be out of date | On |
| [`agents-local-context`](docs/hooks/agents-local-context.md) | Adds applicable `AGENTS.local.md` instructions | On (Codex) |

## Install

Requires macOS on Apple Silicon and Claude Code or Codex.

```sh
curl -fsSL https://github.com/HappyOnigiri/HappyHooks/releases/latest/download/install.sh | bash
hhx install
```

The installer puts `hhx` in `~/.local/bin`; `hhx install` registers its hooks with the agents already configured on your
machine. To build from source, run `make install` before `hhx install`.

## Update

Run `hhx update` to check for a new release, or `hhx update --apply` to install it. Hooks do not check for updates while they run.

## Configuration

Use `~/.config/hhx/config.yaml` to choose a display language or disable a hook:

```yaml
language: ja  # en (default) or ja
hooks:
  idle-wait-guard:
    enabled: false
```

Changes take effect without reinstalling. See [forbidden-term-guard setup](docs/forbidden-terms.md) for its repository-specific list.

Inspect effective settings and their sources with:

```sh
hhx config show
hhx config show pr-merge-guard
hhx config show --json
```

The output lists every hook's enabled state, source and supported agents, plus hook-specific settings, including defaults.
If `HHX_CONFIG` is set, its file is read and the path source is shown.
If the file does not exist, defaults are displayed.
Enabled state refers to hhx configuration; agent registration and Codex trust are not checked.
Repository-specific settings such as forbidden-term lists and temporary session state are outside this command's scope.

JSON returns `path`, `path_source`, `exists`, `language` and `hooks`.
Value sources are `default` / `config`; path sources are `default` / `HHX_CONFIG`.
`hooks[].settings` appears only for hooks with specific settings, with `value` and `source` for each setting.
`markers` lists additional markers only; the standard Go marker is always used.
Keys and state values are independent of display language; the update instruction follows the display language.

Exit codes are `0` for success, `1` for invalid settings or read failures, and `2` for invalid arguments.
Syntax, types, language, hook names and additional marker regular expressions are validated.
Invalid settings produce an error without a settings listing.
The command is read-only and does not reinstall hooks or access the network.

## Contributing

Contributions are welcome. Share bugs and ideas in [Issues](https://github.com/HappyOnigiri/HappyHooks/issues), or send
a [pull request](https://github.com/HappyOnigiri/HappyHooks/pulls). Documentation and translations are welcome too.

## Uninstall

```sh
curl -fsSL https://github.com/HappyOnigiri/HappyHooks/releases/latest/download/uninstall.sh | bash
```
