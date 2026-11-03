# Claude Code の確認プロンプトを実機で確かめる

`dangerous-rm-guard` は Claude Code の組み込み検査が確認プロンプト（ask）を出す形を先に拒否する。
組み込みの判定はバージョンで変わり、ソースからの読み取りだけでは外れることがあるので、判定を緩めたり足したりするときは実機で確かめる。
この文書はその手順と、確かめた結果の記録である。

## 仕組み

- 組み込みの ask は `bypassPermissions` でも出る。出ると PermissionRequest hook が呼ばれるので、その呼び出しを記録すれば ask の有無が分かる。
- 記録用の PermissionRequest hook は `deny` を返し、確認画面で止まらないようにする。
- 利用者の設定（hhx の hook を含む）を読まない。読むと hhx が先に拒否し、組み込みの判定まで届かない。

## 準備

作業用のディレクトリ（`$E` とする）に次の 3 つを置く。

`permreq.sh`（実行権限を付ける）は、呼ばれたコマンドを `permreq.tsv` に記録して拒否する。

```bash
#!/bin/bash
E=$(cd "$(dirname "$0")" && pwd)
payload=$(cat)
printf '%s\t%s\n' "${PERMREQ_TAG:-none}" "$(printf '%s' "$payload" | jq -c '{cmd: .tool_input.command, mode: .permission_mode}')" >> "$E/permreq.tsv"
printf '%s\n' '{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"EXPERIMENT: permission request recorded and denied"}}}'
```

`settings.json` は、記録用の hook だけを登録する（`<E>` は `$E` の絶対パス）。

```json
{"hooks":{"PermissionRequest":[{"matcher":"Bash","hooks":[{"type":"command","command":"<E>/permreq.sh"}]}]}}
```

`run.sh` は、1 つのコマンドを tmux の中の対話モードの Claude Code に 1 回だけ実行させる。

```bash
#!/bin/bash
# usage: run.sh <tag> <cwd> <command-file> [seconds]
E=$(cd "$(dirname "$0")" && pwd)
tag=$1; cwd=$2; cmdfile=$3; dur=${4:-70}
{
  printf 'Run exactly the following Bash command once, verbatim, as a single Bash tool call. '
  printf 'Do not modify it, do not split it, do not run anything else. '
  printf 'If it is denied, do not try alternatives; just quote the exact message you received.\n\n'
  cat "$cmdfile"
} > "$E/prompt-$tag.txt"
tmux new-session -d -s "prq-$tag" -x 220 -y 60 -c "$cwd" \
  "PERMREQ_TAG=$tag claude --model haiku --setting-sources project,local --settings $E/settings.json --permission-mode bypassPermissions \"\$(cat $E/prompt-$tag.txt)\""
end=$((SECONDS+dur))
while [ $SECONDS -lt $end ]; do
  if tmux capture-pane -p -t "prq-$tag" 2>/dev/null | grep -q "❯ No, exit"; then
    tmux send-keys -t "prq-$tag" Down; sleep 0.3; tmux send-keys -t "prq-$tag" Enter
  fi
  sleep 1
done
tmux capture-pane -p -t "prq-$tag" > "$E/pane-$tag.txt"
tmux kill-session -t "prq-$tag"
echo "[$tag] permission requests: $(grep -c "^$tag	" "$E/permreq.tsv")"
```

## 手順

1. 試すコマンドを 1 つずつファイルに書く。削除の対象は `$E` の下の使い捨てのものだけにする（ask が出なければ実際に実行される）。
2. ask が出ることが分かっている対照（`rm -rf .` を使い捨てのディレクトリで実行する）を最初に流し、記録が 1 件付くことを確かめる。
3. `run.sh <tag> <cwd> <command-file>` で 1 件ずつ流す。記録の件数が 0 なら ask は出ていない。
4. Claude Code が実際に実行したコマンドを、セッションの transcript（`~/.claude/projects/<cwd を変換した名前>/<session>.jsonl`）の
   Bash の `tool_use` の `input.command` で確かめる。モデルがコマンドを書き換えたり分けたりした試行は数えない。
5. 同じコマンドを `hhx hook dangerous-rm-guard` に stdin の payload（`tool_name`・`tool_input.command`・`cwd`）で渡し、判定を並べる。

## 落とし穴

- `--setting-sources project,local` を外すと利用者の設定が読まれ、hhx の hook が先に拒否する。
- 初めて開くディレクトリではフォルダの信頼の確認が出る。既定の選択が「No, exit」なので、Enter だけ送るとセッションが終わる。下へ 1 つ動かしてから Enter を送る。
- 対照は先に確かめる。空の変数で始まる `rm -rf $UNSET_XYZ/nonexistent_abc` は、変数が無くても v2.1.317 では ask にならなかった。
- 削除の対象が実在しないと、ask が出ずに実行されても何も起きないので、誤った結論に気付きにくい。対象は実在する使い捨てのものにする。

## 記録

### v2.1.317（2026-11）

| コマンド | ask | hhx（変更後） |
| --- | --- | --- |
| `rm -rf .`（cwd は使い捨て） | 出る | 拒否 |
| `rm -rf $UNSET_XYZ/nonexistent_abc` | 出ない | 通す |
| `echo 'Start by running: cd junk && rm -rf .'` | 出ない | 通す（変更前は拒否） |
| `for c in 'S=/tmp/x; rm -rf $S/$v'; do echo "$c"; done` | 出る | 拒否 |
| `cat > plan.md <<'EOF'`（本文に `$(true)` を 70 個）＋ `rm -f plan.md` | 出ない | 通す（変更前は拒否） |
| 同じ形で、本文にバッククォートを 70 組 | 出ない | 通す（変更前は拒否） |
| `echo '<$(true) を 70 個>' > plan.txt; rm -f plan.txt` | 出ない | 通す（変更前は拒否） |
| `cd <親> && rm -rf <親>/victim`（cwd は victim） | 出る | 拒否 |
| `bash <<'EOF'` / `rm -rf .` / `EOF` | 出ない | 拒否（本文を実行するので止める側に残す） |
