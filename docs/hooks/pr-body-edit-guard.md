# pr-body-edit-guard

English | [日本語](pr-body-edit-guard.ja.md) · [Hook list](../../README.md#hooks)

**Default:** On for Claude Code and Codex.

Before a Bash `gh pr edit` that changes a PR body, the hook checks the proposed body for signs of terminal output.
When it finds a candidate, it reads the current body through `gh pr view`. It checks the lines before or after the
current body's first or last nonblank line, respectively, when that line can be matched uniquely in both versions.

An added block is suspicious if it contains raw ANSI color codes, a diagnostic label followed by an indented command
and two lines of usage guidance, or consecutive diagnostic lines with the same tool prefix.
Markdown quotes and fenced code blocks are excluded from the text checks. Raw ANSI color codes are checked there too.

The refusal identifies the lines and their diagnostic features. If the addition is intentional, use the exact
`HHX_PR_BODY_ACK=<sha256>` assignment shown in the refusal immediately before `gh pr edit` and retry.
The hash applies to the exact proposed body. A change to the body requires a fresh check.

The hook handles direct Bash calls with a literal PR selector, a literal `--body`, or an existing literal
`--body-file` path. An omitted PR selector is resolved by `gh` in the command's working directory.
It skips the comparison if the target or body cannot be resolved, the current body cannot be read, or neither
boundary can be matched. Body input from stdin and files created later in the same shell call are outside its scope.
