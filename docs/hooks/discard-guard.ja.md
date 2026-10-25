# discard-guard

[English](discard-guard.md) | 日本語 · [hook 一覧](../../README.ja.md#hook-一覧)

**既定:** Claude Code と Codex で有効。

未コミットの変更を破棄する Git コマンドの前に、追跡中・未追跡のファイルを `refs/hhx/discard-snapshot` の reflog に保存します。
`git reset --hard`、強制的な checkout・switch、`git restore`、`git clean -f` などが対象です。
保存できればコマンドを通し、対象を特定できない場合や保存に失敗した場合は止めます。
snapshot 自体は作業ツリーと index を変更しません。

## 対象の特定

コマンドの中の `cd` と `git -C` を辿り、破棄するコマンドが走るリポジトリを特定します。
パスはリテラル、`~/` 始まり、同じコマンドの前の方で代入した変数（`S=/abs/path; D=$S/clone; git -C $D reset --hard`）で書けます。
変数を辿るのは、その名前がコマンドの中に 1 回だけ、条件なしに実行される単独の代入として現れる場合だけです。
`if`・`for`・`{ ... }`・サブシェルの中、`&&` や `||` の後、コマンドの前置き（`D=x git ...`）での代入、`$(...)` の結果、
環境変数は辿りません。辿れない対象があれば止めます。

## scratchpad の使い捨ての clone

Claude Code では、セッションの scratchpad（`/tmp/claude-<uid>/<プロジェクト>/<session_id>/scratchpad`）の中での破棄は
snapshot を取らずに通します。エージェントが検証用の使い捨ての clone を作る場所だからです。
同じコマンドで先に clone する形のように、hook の実行時点で対象がまだ無くても通します。
その場合、そこへの `cd` は破棄するコマンドと `&&` でつなぐか、`set -e` の下で単独の行に置く必要があります。
`cd` が失敗したときに別のリポジトリで破棄しないためです。symlink は解決してから判定し、作業ツリーが scratchpad の外にある対象は
snapshot を取ります。`ln` を実行するコマンドでは、まだ無いパスを scratchpad の中とみなしません。
判定は Claude Code の scratchpad のパスの形で行い、Codex のセッションでは行いません。

ファイルへ書き出すヒアドキュメント（`cat > x.sh <<'EOF' ... EOF`、`tee x.sh <<EOF`）の本文は、そのコマンドの中では実行されないので
検出の対象から外します。スクリプトをすぐ実行する形のように書き出したファイルの名前がコマンドの中にもう一度現れる場合と、
クォートしていない本文が `$(...)` やバッククォートを含む場合は、本文も検出します。
シェルへ流し込むヒアドキュメント（`bash <<'EOF'`）は常に検出します。

## snapshot の復元

作業ツリーのパスが付いたエントリを探し、その番号を `N` に入れます。

```sh
git reflog show --format='%gd %gs' refs/hhx/discard-snapshot
git restore --source='refs/hhx/discard-snapshot@{N}' --worktree -- .
```

復元するのは作業ツリーで、index は変更しません。無視されたファイルと submodule 内の未コミットの変更は保存されません。snapshot は reflog とともに期限切れになります。
