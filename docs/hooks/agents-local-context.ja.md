# agents-local-context

[English](agents-local-context.md) | 日本語 · [hook 一覧](../../README.ja.md#hook-一覧)

**既定:** Codex のみ有効。

Git ルートからツールの対象パスまで、各ディレクトリでルールを探します。
同じディレクトリに `AGENTS.md` がなければ `CLAUDE.md` を読み込みます。
続いて `AGENTS.local.md` を探し、なければ `CLAUDE.local.md` を読み込みます。

セッション開始時とツール実行前にルールを探し、compact の後と subagent の起動時には記録済みの場所からルールを読み直します。

同じセッションで内容が変わらないルールは、通常 1 回だけ注入します。ツール入力からパスを見つけるのはベストエフォートで、変数・クォート・heredoc はパスを隠したりパスに見えたりします。コンテキストを足すだけで、ツールは止めません。
