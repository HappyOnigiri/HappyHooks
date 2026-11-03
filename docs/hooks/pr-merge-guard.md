# pr-merge-guard

English | [日本語](pr-merge-guard.ja.md) · [Hooks](../../README.md#hooks)

**Default:** On for Claude Code and Codex.

Blocks agent commands that merge a GitHub pull request. This includes `gh pr merge` and merge REST and GraphQL calls.
It does not block ordinary PR inspection (including fetching `pull/N/head`) or pushing a
branch.

To allow merges for one session, start the agent CLI with `AGENT_ALLOW_PR_MERGE=1`. Setting it inside an agent-run shell does not change the hook's environment.

The guard checks command text, so it cannot recognize every command hidden behind variables, scripts, or another shell.
