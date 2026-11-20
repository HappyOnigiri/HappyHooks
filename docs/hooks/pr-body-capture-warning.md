# pr-body-capture-warning

English | [日本語](pr-body-capture-warning.ja.md) · [Hook list](../../README.md#hooks)

**Default:** On for Bash in Claude Code and Codex, and REPL in Claude Code.

Before a tool call, this hook warns the agent if a recognizable `gh pr view` or `gh api` command fetches a PR body
and merges stderr into the same output. It also warns when Claude Code's REPL captures a PR body with `.text()`
without redirecting stdout to a file. The warning does not interrupt the tool call.

To reuse a body for an update, write stdout to a file, then read that file or pass it to `--body-file`.

```sh
gh pr view "$pr" --json body --jq .body > "$body_file"
```

Ordinary PR viewing and shell captures that keep stdout and stderr separate pass without a warning.
