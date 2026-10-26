# permission-request-guard

English | [日本語](permission-request-guard.ja.md) · [Hooks](../../README.md#hooks)

**Default:** On for Claude Code only.

Denies every Bash command for which Claude Code asks the user for approval, instead of leaving the user waiting at the
prompt. The hook runs on Claude Code's `PermissionRequest` event, after Claude Code has decided to ask, so it does not
parse the command. It catches what [`dangerous-rm-guard`](dangerous-rm-guard.md) misses, such as checks added in newer
Claude Code versions.

What it denies depends on the permission mode:

- In bypass mode, the prompts come mostly from Claude Code's safety checks, such as a dangerous `rm` or syntax its
  parser cannot read. Commands matched by your `ask` permission rules are denied too.
- In other modes, every Bash command that your permission rules do not allow is denied as well. Add the commands you
  want to run to the allow rules, or disable this hook.

The agent receives a reason that tells it to rewrite the command with explicit, narrower targets, or to stop and report.

Each denied command is appended to `~/.cache/hhx/permission-request-guard/denied.jsonl` (`$XDG_CACHE_HOME` if set) with
the time, hhx version, session and agent IDs, permission mode, working directory, command, and the
`permission_suggestions` Claude Code sent. In default mode, a safety-check prompt arrived with empty suggestions while
an ordinary unapproved command came with some, which helps filter the log. Use it to find commands
that `dangerous-rm-guard` should deny earlier. When the file grows past 1 MiB, it is moved to `denied.jsonl.1`.

Limits:

- Claude Code shows the approval prompt while the hook runs, typically for less than a second. An answer given during
  that time, including pressing Enter, wins over the hook.
- If the hook fails, it prints nothing and the prompt stays as before.
- Codex has no matching event, so the hook is not registered there.
