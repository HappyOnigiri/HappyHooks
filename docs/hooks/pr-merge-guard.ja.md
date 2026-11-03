# pr-merge-guard

[English](pr-merge-guard.md) | 日本語 · [hook 一覧](../../README.ja.md#hook-一覧)

**既定:** Claude Code と Codex で有効。

エージェントによる GitHub の PR のマージを止めます。`gh pr merge`、マージ用の REST・GraphQL 呼び出しが対象です。通常の PR の確認（`pull/N/head` の取得を含む）やブランチへの push は止めません。

1 セッションだけ許可する場合は、`AGENT_ALLOW_PR_MERGE=1` を付けてエージェントの CLI を起動します。エージェントが実行するシェル内で設定しても、hook の環境には反映されません。

コマンド文字列を検査するため、変数やスクリプト、別のシェルの中に隠れた操作はすべて検出できるわけではありません。
