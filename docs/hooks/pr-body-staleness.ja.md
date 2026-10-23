# pr-body-staleness

[English](pr-body-staleness.md) | 日本語 · [hook 一覧](../../README.ja.md#hook-一覧)

**既定:** Claude Code と Codex で有効。

`git push` の成功後、PR 本文の最終編集日時と commit の日時を比べます。本文より新しい commit があれば、現在のタイトルを示し、タイトルと本文が現在の変更と合っているかをエージェントに確認させます。新しい commit があるだけで本文を古いと断定しません。比べるのは本文の最終編集日時なので、タイトルだけを直しても確認の対象から外れません。

既定の案内は `update-pr` スキルを参照します。変更するには `~/.config/hhx/config.yaml` に `hooks.pr-body-staleness.update-instruction` を書きます。

```yaml
hooks:
  pr-body-staleness:
    update-instruction: "本文が古ければ gh pr edit で更新する。"
```

push 直後の GitHub は、PR のコミット一覧をまだ push 前の状態で返すことがあります。PR の head が push した `HEAD` と一致するまで 2 秒ごとに取り直し、一致した時点の一覧で判定します。待つのは全体で最大 20 秒で、push した `HEAD` に open の PR が無ければ待ちません。

20 秒を過ぎても一致しなければ、最後に取得した一覧で判定し、いま push したコミットが一覧に含まれていない可能性があると添えます。一覧が古いままでは「本文より新しいコミットが 0 件」という結果も信用できないため、0 件でもこの案内を出します。

認証済みの `gh` を呼び、PR を取得できなければ（取り直しで失敗した場合を含む）黙って終了します。rebase や cherry-pick で commit の日時が変わると、本文が正しくても確認を促すことがあります。

現在の `HEAD` 以外のブランチを push したときや、`HEAD` が別の open の PR（積み重ねたブランチなど）にも含まれていてそちらが先に返ったときは、head が一致しないので 20 秒待ち、一覧が遅れている可能性として案内します。
