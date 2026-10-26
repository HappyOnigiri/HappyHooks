# permission-request-guard

[English](permission-request-guard.md) | 日本語 · [hook 一覧](../../README.ja.md#hook-一覧)

**既定:** Claude Code のみ有効。

Claude Code がユーザーに承認を求めた Bash コマンドを、承認待ちにせずすべて拒否します。Claude Code が確認を出すと決めた後の
`PermissionRequest` で動くので、コマンドは解析しません。Claude Code の新しい版で増えた検査など、
[`dangerous-rm-guard`](dangerous-rm-guard.ja.md) がすり抜けたものを受け止めます。

拒否するものは権限モードで変わります。

- bypass モードで確認になるのは、主に危険な `rm` やパーサーが読めない構文など、Claude Code の安全確認です。権限の `ask` のルールに一致したコマンドも拒否します。
- それ以外のモードでは、権限のルールで許可していない Bash コマンドもすべて拒否します。実行させたいコマンドは allow のルールに足すか、この hook を無効にしてください。

エージェントは、対象を明示して狭めたコマンドに書き換えるか、作業を止めて報告するよう促す理由文を受け取ります。

拒否したコマンドは `~/.cache/hhx/permission-request-guard/denied.jsonl`（`$XDG_CACHE_HOME` があればその下）に、
時刻・hhx の版・セッションとエージェントの ID・権限モード・作業ディレクトリ・コマンド・Claude Code が渡した
`permission_suggestions` を 1 行ずつ追記します。default モードでは、安全確認による確認では suggestions が空で、
普通の未承認のコマンドでは候補が付いていたので、記録を絞り込む手がかりになります。
`dangerous-rm-guard` で先に止めるべき形を探すのに使います。1 MiB を超えると `denied.jsonl.1` へ退避します。

制約:

- hook の実行中も Claude Code は確認画面を表示します（通常は 1 秒未満）。その間の回答（Enter を含む）は hook より優先されます。
- hook が失敗すると何も出力せず、従来どおり確認画面が残ります。
- Codex には対応するイベントが無いため、登録しません。
