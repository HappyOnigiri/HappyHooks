# forbidden-term-guard

English | [日本語](forbidden-term-guard.ja.md) · [Hooks](../../README.md#hooks)

**Default:** On for Claude Code and Codex; it does nothing until the repository has a term list.

Checks PR and issue bodies sent through `gh` for repository-specific forbidden terms. It also reads a body file passed
to a matching command. A match blocks the submission and identifies the affected line. Read-only `gh api` requests
(GET, or GraphQL queries without `mutation`) are not checked, because they send nothing to be published.

Put `forbidden-terms.txt` in the repository's common Git directory, outside tracked files. List one term per line; use
`re:` for regular expressions. See the [list format and covered commands](../forbidden-terms.md) (Japanese) for the full
rules.
