# Happy Hooks

[English](README.md) | 日本語

Happy Hooks は、Claude Code と Codex が長時間の作業を滞りなく進めるための hook 集です。`hhx` という 1 つの Go バイナリで動作します。

## 特長

- **作業を止めない:** 不要な確認プロンプトや、作業を中断させるよくある誤りを防ぎます。
- **効率よく待つ:** ジョブごとに何度も確認せずに CI を待つ方法をエージェントに案内します。
- **必要な情報を渡す:** PR の情報やローカルの指示を、作業中の適切な場面で注入します。

コマンドを検査する hook は、静的な判定でよくある誤りを止めます。セキュリティ境界ではありません。

## hook 一覧

| hook | 役割 | 既定 |
| --- | --- | --- |
| [`pr-merge-guard`](docs/hooks/pr-merge-guard.ja.md) | エージェントによる PR のマージを止める | 有効 |
| [`discard-guard`](docs/hooks/discard-guard.ja.md) | Git で変更を破棄する前に snapshot を保存する | 有効 |
| [`idle-wait-guard`](docs/hooks/idle-wait-guard.ja.md) | 時間を埋めるだけのコマンドを止める | 有効 |
| [`forbidden-term-guard`](docs/hooks/forbidden-term-guard.ja.md) | PR・issue の本文に設定済みの禁止語があれば止める | 有効 |
| [`irreversible-guard`](docs/hooks/irreversible-guard.ja.md) | 元に戻せない操作を止める | 有効 |
| [`generated-edit-guard`](docs/hooks/generated-edit-guard.ja.md) | 自動生成ファイルの手編集を止める | 有効 |
| [`dangerous-rm-guard`](docs/hooks/dangerous-rm-guard.ja.md) | Claude Code の確認待ちになる危険な `rm` を先に止める | 有効（Claude Code） |
| [`exit-plan-subagent-guard`](docs/hooks/exit-plan-subagent-guard.ja.md) | バックグラウンドのエージェントが終わるまでプランモードを維持する | 有効（Claude Code） |
| [`git-hookspath-guard`](docs/hooks/git-hookspath-guard.ja.md) | Git の hook 設定の変更を止める | 無効 |
| [`pr-context`](docs/hooks/pr-context.ja.md) | プロンプト中の PR の情報を注入する | 有効 |
| [`push-ci-context`](docs/hooks/push-ci-context.ja.md) | push 後に CI の待ち方を案内する | 有効 |
| [`pr-body-staleness`](docs/hooks/pr-body-staleness.ja.md) | PR の本文が古い可能性を知らせる | 有効 |
| [`agents-local-context`](docs/hooks/agents-local-context.ja.md) | 適用される `AGENTS.local.md` の指示を注入する | 有効（Codex） |

## インストール

Apple Silicon の macOS と Claude Code または Codex が必要です。

```sh
curl -fsSL https://github.com/HappyOnigiri/HappyHooks/releases/latest/download/install.sh | bash
hhx install
```

インストーラーは `hhx` を `~/.local/bin` に置きます。`hhx install` は設定済みのエージェントに hook を登録します。ソースからビルドする場合は、`hhx install` の前に `make install` を実行します。

## 更新

新しい版の確認は `hhx update`、適用は `hhx update --apply` で行います。hook の実行中に更新を確認することはありません。

## 設定

`~/.config/hhx/config.yaml` で表示言語を選び、hook を無効にできます。

```yaml
language: ja  # en（既定）か ja
hooks:
  idle-wait-guard:
    enabled: false
```

変更に再インストールは不要です。リポジトリごとの禁止語リストは[forbidden-term-guard の設定](docs/forbidden-terms.md)を参照してください。

現在使われる設定値と出所は、次のコマンドで確認できます。

```sh
hhx config show
hhx config show pr-merge-guard
hhx config show --json
```

既定値を含む全 hook の有効・無効、値の出所、対象エージェントと hook 固有の設定を表示します。
`HHX_CONFIG` が設定されている場合は、そのファイルを読み、出力に指定元を示します。
設定ファイルがなければ既定値を表示します。
有効・無効は hhx の設定上の状態で、エージェントへの登録や Codex の信頼状態は確認しません。
禁止語リストなどのリポジトリ別設定や、セッションの一時的な状態も対象外です。

JSON では `path`、`path_source`、`exists`、`language` と `hooks` を返します。
値の出所は `default` / `config`、パスの指定元は `default` / `HHX_CONFIG` です。
`hooks[].settings` は固有設定がある hook だけに付き、各項目を `value` と `source` で示します。
`markers` は追加マーカーだけで、Go の標準マーカーは常に使用します。
キーと状態の値は表示言語によらず固定し、更新案内の文面は表示言語に従います。

正常終了は `0`、不正な設定・読み込み失敗は `1`、不正な引数は `2` です。
構文・型・表示言語・hook 名・追加マーカーの正規表現を検査し、不正な場合は一覧を出さずにエラーを報告します。
設定の確認は読み取り専用で、再インストールやネットワーク通信は行いません。

## コントリビュート

コントリビュートを歓迎します。バグ報告や提案は [Issues](https://github.com/HappyOnigiri/HappyHooks/issues) に、変更は [プルリクエスト](https://github.com/HappyOnigiri/HappyHooks/pulls) でお寄せください。文書や翻訳の改善も歓迎します。

## アンインストール

```sh
curl -fsSL https://github.com/HappyOnigiri/HappyHooks/releases/latest/download/uninstall.sh | bash
```
