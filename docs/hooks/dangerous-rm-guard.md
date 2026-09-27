# dangerous-rm-guard

English | [日本語](dangerous-rm-guard.ja.md) · [Hooks](../../README.md#hooks)

**Default:** On for Claude Code only.

Denies forms of `rm` and `rmdir` that Claude Code's built-in safety check would turn into a confirmation prompt.
Examples include a path starting with a possibly empty variable, a target that cannot be resolved statically, a critical
directory, or the current working directory and its ancestors.

The agent receives a reason it can act on instead of leaving the user waiting at a prompt.
The main rules mirror Claude Code v2.1.239. Deletion through a variable derived from an earlier assignment,
such as `D=$S/$n; rm -rf $D`, follows v2.1.283. Other checks may differ from newer versions.
