# blocking-command-guard

[English](blocking-command-guard.md) | 日本語 · [hook 一覧](../../README.ja.md#hook-一覧)

**既定:** Claude Code と Codex で有効。

コマンドの字面から分かる入力待ち・対話操作・終了しない前景実行を、PreToolUse で拒否します。
遅い処理や無出力の処理を監視して止める機能ではありません。タイムアウトは追加しません。

## 対象

| 種類 | 対象の例 |
|---|---|
| 入力元のない読み取り | `cat`、`tee OUTPUT`、`sort`、`wc -l`、`head`、`grep needle`、`sed -n p`、`read -r line` |
| 入力元のないスクリプト実行 | `python3 -`、`python3`、`sh -s`、引数なしの `node`・`ruby`・`perl` |
| 明示的な対話モード | `git add -p`、`git rebase -i HEAD~2`、`git commit --edit`、`python3 -i`、`node -i` |
| 対話操作を目的とするコマンド | オプションなしの `vi`・`vim`・`nvim`・`nano`・`emacs`・`top`・`htop`・`fzf` |
| 継続する前景実行 | `tail -f FILE`、`tail -F FILE`、`watch date`、`yes` |

表のコマンドでも、引数・入力元・実行方法によっては通します。
対象は標準的なコマンド名と `/bin/`・`/usr/bin/` の同名コマンドです。任意のパスにある同名のスクリプトは対象外です。

## 入出力とバックグラウンド実行

構文木でパイプ、heredoc、入力リダイレクト、ブロックとサブシェルを区別します。
次の形は入力元が指定されているため通します。

```sh
cat < input.txt
printf data | cat
python3 - <<'PY'
print('ok')
PY
```

`exec < input.txt; cat` の入力元も追跡します。
`> OUTPUT` と `2>/dev/null` は入力元とは扱いません。
`cat > OUTPUT; python3 ...` のように、途中に入力待ちがあるコマンド列も拒否します。
拒否時にはコマンド全体が実行されないため、出力ファイルの切り詰めや後続処理の遅延実行も起きません。

明示的な `&`、または Bash ツールの `run_in_background: true` は通します。
パイプの下流が継続処理を終了させる可能性もあるため、`tail -f FILE | head -n 1` や `yes | head -n 1` は通します。
これは終了を保証する判定ではありません。曖昧な形を拒否しないための扱いです。

## 誤検知を避けるための範囲

次の形は判定を推測せず通します。

- 解析できない構文、128 KiB を超えるコマンド、変数展開・コマンド置換・glob を含む呼び出し。
- 関数定義、変数代入、alias、`source`、`eval`、`trap`、`exit`・`return` を含むコマンド列。
- `set`・`export`・`command` など、シェルの設定・宣言・コマンド解決を変更する命令を含むコマンド列。
- `if`・ループ・`case` の中身と、それらが入力元を変えた可能性がある後続処理。
- 未知のオプション、`vim -es`・`nvim --headless`・`emacs --batch`・`top -l 1`・`fzf --filter` などの有限・非対話モード。
- TTY や設定に依存するページャー、認証、メッセージ未指定の `git commit`。`less FILE`・`more FILE`・通常の `git log` は拒否しません。

`grep`・`sed` のスクリプトは限定した形だけを扱います。`awk` のスクリプトや、`sh -c`・スクリプトファイルの内部は解析しません。
ファイル・FIFO・端末など入力元自体が待つかどうかも推測しません。
コマンドの起動に失敗するか、既存の設定や外部状態によって早く終了するかまでは保証しません。

## 修正方法

拒否理由には `STDIN`・`INTERACTIVE`・`EDITOR`・`FOLLOW`・`STREAM` の分類とコマンド全体を表示します。
入力元や必要な値を明示し、非対話で終了するモードを使ってください。不要なコマンドは削除します。
意図した継続処理はバックグラウンド実行を明示できます。

待機を避けるためだけにタイムアウトを追加したり、削除・公開などを自動承認したりすることは案内しません。
実行中のプロセスの監視・強制終了、標準入力の `/dev/null` への変更、コマンドの自動書き換えは行いません。
字面で判断できない待機は、今回の対象外です。
