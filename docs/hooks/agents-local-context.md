# agents-local-context

English | [日本語](agents-local-context.ja.md) · [Hooks](../../README.md#hooks)

**Default:** On for Codex only.

Checks for rules from the Git root down to a tool's target path. In each directory it adds `CLAUDE.md` when
`AGENTS.md` is absent. It then adds `AGENTS.local.md` if present or `CLAUDE.local.md` if that path is absent.
The hook checks on session start and before tool use, and checks the recorded locations again after compaction
and for subagents.

Within a session, unchanged rules are normally injected only once. The hook reads paths from tool input on a best-effort
basis, so variables, quoted text, and heredocs can hide a path or resemble one. It adds context without blocking a tool.
