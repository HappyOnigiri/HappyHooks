# push-ci-context

[English](push-ci-context.md) | 日本語 · [hook 一覧](../../README.ja.md#hook-一覧)

**既定:** Claude Code と Codex で有効。

`main` 以外への `git push` または `gh pr create` の成功後、残りの作業を先に終えるよう案内します。
その後 `hhx wait-ci --progress` で PR の check の終了を待ち、結果を 1 回だけ報告します。
短い間隔で繰り返し確認することや、CI を見ずにターンを終えることを避けます。
`main` への直接 push では案内しません。
`main` 上で引数なしの `hhx wait-ci` を実行した場合も、PR の CI が無いことを伝えて即時終了します。
detached HEAD から `main` に直接 push し、`HEAD` とローカルの `origin/main` が一致する場合も同様です。
detached HEAD のときと、今のブランチに PR が無いとき（`git push origin HEAD:<別のブランチ>` の後など）は、`hhx wait-ci` が HEAD の commit から PR を探します。
今のブランチを同じ名前で push 済みで `origin/<branch>` が `HEAD` と一致するときは、PR がまだ作られていないので探さずにスキップします。

`gh workflow run` の成功後は `gh run watch` で対象の workflow を待つよう案内します。失敗した push や dry-run の push では発火しません。コンテキストを足すだけで、権限の判断は返しません。
