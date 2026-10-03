# git-hookspath-guard

English | [日本語](git-hookspath-guard.ja.md) · [Hooks](../../README.md#hooks)

**Default:** On for Claude Code and Codex.

For setups that delegate global Git hooks to repository hooks, this guard blocks changes to `core.hooksPath` and direct
edits to Git configuration files such as `.git/config`. Reading Git configuration is allowed.

Disable it in `~/.config/hhx/config.yaml`:

```yaml
hooks:
  git-hookspath-guard:
    enabled: false
```
