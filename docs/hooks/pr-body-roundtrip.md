# pr-body-roundtrip

English | [日本語](pr-body-roundtrip.ja.md) · [Hooks](../../README.md#hooks)

**Default:** On for Claude Code.

After a successful Bash `gh pr create` or `gh pr edit` with `--body-file` (`-F`), the hook reads the body file and uses
`gh pr view` to fetch the saved body. It ignores CRLF and trailing newline differences. When the text differs, it shows
an excerpt of the unified diff and asks the agent to resend the intended body and verify the result on GitHub.

For `gh pr create`, the tool output must contain the URL of the new PR. For `gh pr edit`, the hook uses the specified PR
number, URL, or branch, or lets `gh` find the PR for the current branch. It respects `-R` (`--repo`) and can follow a
leading `cd` with an absolute path that contains no `..`.

The hook stays silent if it cannot identify the command, PR, or file, or if `gh pr view` fails or times out. Commands
using standard input (`--body-file -`), shell expansions, or attachment uploads are outside the check. The hook reads
the file after the command finishes, so keep the file available and unmodified until the check runs.
