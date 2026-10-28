# バージョンとリリース

## バージョンの真実源

`hhx` のバージョンはリリースタグ `vX.Y.Z` だけが持つ。
リポジトリにバージョンを書いた manifest を置かないので、リリース PR はバージョンを刻む差分の無い空コミットになる（共通 Action の `version-bump-type` を `none` にしている）。

開発ビルドは `git describe` の結果を `-ldflags` で `internal/version` へ埋め込み、`BuildMeta` を `dev` にする。
手元の `make build`・`make install` が作るのは常に開発ビルドで、`-dev` の手前が基にしたリリースを示す。
タグを取得していない shallow checkout ではコミットの略記に退避するので、CI でも `hhx version` は空にならない。

`make version-check` はこの表示が `VERSION` と完全に一致することを確かめる。
接頭辞だけの確認では、`-X` のパスが変わって `undefined` のままでも気付けないためである。

## リリースの流れ

準備と公開は [HappyOnigiri/ReleaseActions](https://github.com/HappyOnigiri/ReleaseActions) の共通 Action に任せ、hhx 側のワークフローは入力の受け口と成果物のビルドだけを持つ。

1. `release.yml` は定期実行または手動実行で、リリースブランチ（`release/vX.Y.Z`）と changelog を本文に持つリリース PR を作る。
   定期実行と `automatic` を有効にした手動実行では、対象 PR がない場合や既存のリリース PR がある場合はスキップし、作成した PR は CI 成功後に同じコミットを指定して自動マージする。
2. その PR を main へ merge すると、`publish-release.yml` が merge コミットを checkout して `make release` でビルドし、同じコミットにタグを打って 4 つの成果物を添付した Release を公開する。
   ビルドに使うタグはリリースブランチ名から取り出すので、成果物のバージョンは PR のブランチ名で決まる。
3. main 宛の PR には、`release-reminder.yml` が前回のリリース以降に merge された PR の数を知らせる。

- `bump: auto` は前回のリリース以降に merge された PR のタイトルから上げ幅を決め、breaking change を見つけたらエラーで止まる。
  major は `bump: major` か `version` の明示でしか上がらない。
- Publish Release は同時実行を直列化し、途中の実行を取り消さない。
  添付に失敗したら draft のまま残り、同じワークフローの再実行で続けられる。

### 最初のリリース

最初のリリースでは、`bump: auto` は main の全マージ済み PR からバージョンを判定する。
バージョンを指定したい場合は、`release.yml` の `version` に semver を直接指定して実行する。

```sh
gh workflow run release.yml -f version=0.1.0
```

最初の Release を公開するまで、README の固定 URL（`releases/latest/download/install.sh`）からはインストーラーを取得できない。

### リポジトリに要る設定

- repository secret `GH_TOKEN`: `contents:write` と `pull-requests:write` を持つ PAT。`release.yml` と `publish-release.yml` が使う。
  `release-reminder.yml` は `GITHUB_TOKEN` で足りる。
- `HappyOnigiri/ReleaseActions` を hhx のワークフローから参照できること（公開リポジトリなら追加の設定は要らない）。

## 配布用ビルド

`make release` はタグを `RELEASE_VERSION` で必ず受け取り（開発ビルドの `VERSION` の推定は使わない）、`RELEASE_DIR`（既定は `artifacts/release`）へ次の 4 つを作る。

| ファイル | 内容 |
| --- | --- |
| `hhx-darwin-arm64` | macOS arm64 向けのバイナリ（`CGO_ENABLED=0`、`BuildMeta` は空） |
| `checksums.txt` | バイナリの sha256 |
| `install.sh` | `scripts/install.sh` にタグを埋め込んだもの |
| `uninstall.sh` | `scripts/uninstall.sh` をそのまま（何もダウンロードしない） |

`publish-release.yml` はこの 4 つのパスを固定で参照するので、`RELEASE_DIR` やファイル名を変えると添付が黙って壊れる。
`make release-check` は配布物の OS・CPU・CGO とチェックサム、インストーラーへのタグの差し込みを検査し、macOS arm64 の手元ではバージョンの表示まで確かめる。
CI のランナーは ubuntu だけなので、表示の確認は手元の `make ci` でだけ走る。

## インストールと更新

README は `releases/latest/download/` の固定 URL を案内し、インストーラーには CI が生成時のタグを埋め込む。
取得するバイナリとチェックサムを同じタグへ固定できるので、次のリリースが途中で公開されても取得物は混ざらない。

`install.sh` は macOS arm64 を対象にし、checksum の検証、`hhx version` とタグの照合、同じファイルシステムの一時ファイルからの `mv` による置き換えを行う。
取得や検証が失敗したら既存のバイナリに触らない。
hook の登録（`hhx install`）は呼ばず、初回の導入では案内だけを出す。
更新では、登録に書いた絶対パスがそのまま使えるので何もしない。

`hhx update` は未認証の Releases API で最新のタグを確かめ、`--apply` でそのタグの `install.sh` を取得して実行する。
checksum の検証・版の照合・atomic な置換はすべて `install.sh` が持ち、Go の側に同じ処理を二重に書かない。
インストーラーは新しいセッションで、標準入力を閉じ、環境を許可リスト（`PATH`・`HOME`・プロキシと CA の変数）で渡して実行する。
置き換えが起きたかは、インストーラーの `Installed hhx <tag> to <path>` の報告で確かめる。

- 確認は `hhx update` を明示的に実行したときだけ行う。hook は全ツール呼び出しで走るので、hook の実行中にネットワークへ出てはいけない。
- 開発ビルドでは確認も更新もしない。埋め込み版が `vX.Y.Z` ではなく比較できず、`install.sh` の置き換え先が開発用の配置と一致するとも限らないためである。
- レート制限と未公開は他の失敗と分けて表示する。

## アンインストール

`uninstall.sh` は `hhx uninstall`（hook の登録の削除）を呼んでから `~/.local/bin/hhx` を消す。
順序を逆にすると、agent が存在しないコマンドを hook として呼び続ける。
`hhx uninstall` が失敗したらバイナリを残して止まる。
設定（`~/.config/hhx/`）・キャッシュ（`~/.cache/hhx/`）・設定のバックアップ（`~/.local/state/hhx/`）は消さずに場所を表示し、リポジトリに残る `refs/hhx/` の消し方を案内する。
