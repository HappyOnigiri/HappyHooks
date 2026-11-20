# pr-body-capture-warning

[English](pr-body-capture-warning.md) | 日本語 · [hook 一覧](../../README.ja.md#hook-一覧)

**既定:** Claude Code と Codex の Bash、および Claude Code の REPL で有効。

`gh pr view` や `gh api` で PR 本文を取得するとき、本文と stderr を同じ出力先に送る書き方を実行前にエージェントへ知らせます。
Claude Code の REPL で、stdout をファイルに分けずに `.text()` で取り込む場合も警告します。
警告が出てもツール呼び出しは実行されます。

取得した本文を PR の更新に使うときは、stdout をファイルに書き出してから、そのファイルを読むか `--body-file` に渡します。

```sh
gh pr view "$pr" --json body --jq .body > "$body_file"
```

通常の PR の閲覧や、シェルで stdout と stderr を分けて取り込む書き方では警告しません。
