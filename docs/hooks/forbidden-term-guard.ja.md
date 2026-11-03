# forbidden-term-guard

[English](forbidden-term-guard.md) | 日本語 · [hook 一覧](../../README.ja.md#hook-一覧)

**既定:** Claude Code と Codex で有効。リポジトリに禁止語リストがなければ何もしません。

`gh` で送る PR や issue の本文に、リポジトリ固有の禁止語が含まれていないかを調べます。対象のコマンドに渡された本文ファイルも読みます。一致すると送信を止め、該当行を示します。読み取りだけの `gh api`（GET と、`mutation` を含まない GraphQL の query）は、公開先へ何も書き込まないので調べません。

`forbidden-terms.txt` を、Git の管理外にあるリポジトリの共通 Git ディレクトリへ置きます。1 行に 1 語を書き、正規表現には `re:` を付けます。詳しくは[リストの書式と対象コマンド](../forbidden-terms.md)を参照してください。
