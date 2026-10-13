# AGENTS.md

`hhx` は、Claude Code と Codex の agent hook を 1 つのバイナリで提供する Go 製 CLI である。
hook は 1 本につき 1 エントリ（`hhx hook <name>`）で登録し、中身の差し替えで登録を変えない。

## 不変条件

- `hhx hook <name>` はどの経路でも終了コード 0 で終わる（名前を省いた手入力は usage を出して 2）。未知の名前・壊れた設定・本体のエラーや panic は無出力にする（fail-open）。
- PreToolUse を通すときは判断を出力しない。`allow` と `ask` は返さない（`ask` は Codex が解釈しない）。
  注入系の hook（agents-local-context など）は判断のフィールドを持たない additionalContext と systemMessage だけを返す。
- `hhx install` / `uninstall` は hhx のエントリ（先頭トークンの basename が `hhx`、2 番目が `hook`）だけを扱う。
  wx や利用者の hook は、同じグループにも入れないし消しもしない。
- install は冪等で、hhx のエントリに変化が無ければファイルに触らない。
  既存の hhx のグループは同じ位置へ置き直す。Codex は hooks.json の中の位置で hook の信頼状態を記録するため、位置が動くと再承認が要る。
- 設定ファイルに任意項目の `null` を書かない。matcher を持たないグループに不正なエントリが 1 つでもあると、グループごと無視される。
- 各 hook のコマンド解析（シェルのトークナイザ）は共通化しない。hook ごとに誤爆の条件が調整されており、共通化すると判定が変わり得る。
  共通化するのは `internal/hookrt` の payload の読み込み・出力スキーマ・実行時の保護と、
  `internal/pycompat` の Python の文字列・正規表現の意味の再現だけとする。
  Python の `shlex` の字句解析そのもの（`pycompat.ShlexSplit`）は再現の部品として共有してよいが、
  空白と区切りの文字、区切った後のトークンの解釈は hook ごとに決める（push-ci-context と agents-local-context で設定が違う）。
- 全 Bash 呼び出しで走るため、一次ゲート（生の入力の部分一致）を設定の読み込みより前に置き、そこまでを軽く保つ。
  agents-local-context は移植元と同じく一次ゲートを持たず、Codex の全 PreToolUse で判定する（git の呼び出しは実行の中で使い回す）。

## 構成

- `cmd/hhx`: サブコマンドの振り分け
- `internal/hookrt`: hook の定義と実行時の共通処理
- `internal/registry`: hook の一覧。install と `hhx hook` の振り分けはここから作る
- `internal/hooks/<name>`: hook 本体。1 hook 1 パッケージ（下の「hook の移植の型」）
- `internal/pycompat`: Python の `\s`・`\d`・`\b`・`str.split()`・`splitlines()`・`str.lower()`・`json.dumps`・`os.path`・`shlex` の再現
- `internal/hookexec`: hook から git・gh を時間の上限付きで起動する。gh は PATH から探すので、テストでは偽の gh で差し替えられる
- `internal/hookcache`: 失っても害のない hook の状態（取得のキャッシュ・注入済みの記録）の置き場（`$XDG_CACHE_HOME/hhx/<hook>`）
- `internal/toolresponse`: PostToolUse の実行結果（tool_response）の成功の判定。push-ci-context と pr-body-staleness が共有する
- `internal/hooktest`: hook のテストの補助（テストからだけ使う）。偽の gh と、
  HOME・作業ディレクトリ・git の設定を隔離する `Main` を持つ
- `internal/install`: Claude の settings.json と Codex の hooks.json の読み書き
- `internal/config`: `~/.config/hhx/config.yaml` の読み込み（表示言語 `language` を含む）
- `internal/i18n`: 表示言語（`en` / `ja`、既定は英語）と、ID ごとに英語と日本語を持つカタログ。各パッケージの `messages.go` が自分の表を `Register` する
- `internal/update`: GitHub Releases の確認と、Release 添付の `install.sh` による更新（`hhx update`）
- `internal/waitci`: `hhx wait-ci`（PR の CI の完了を 1 回だけ報告する）の判定の本体。引数の解析と出力は `cmd/hhx/waitci.go` が持つ。
  hook ではないので hook の実行時の保護（fail-open）を通さない。1 行目の結論・最終行の `wait-ci: exit=...`・終了コードは読み手との契約なので変えない。
- `scripts/`: 配布物のビルド、インストーラーとアンインストーラー、それらのテスト
- `.github/workflows/`: CI とリリース（[docs/release.md](docs/release.md)）、flaky なテストの起票（`report-flaky-tests.yml`）
- `.github/scripts/`: flaky なテストの issue を起票する reporter（`actions/github-script` から呼ぶ）
- `tools/`: 開発用の補助ツール。`tools/citest` は CI のテストで落ちたテストだけを 1 回再実行し、報告を artifact に残す

## 開発

- CI と同じ検査は `make ci`（lint・race とカバレッジのテスト・日本語の表示でのテスト・配布物・インストーラーの検査を並列に走らせる）。
- CI のジョブは Makefile のターゲットを 1 つずつ走らせる。検査を足すときは Makefile に書き、`ci-checks` と `ci.yml` の matrix の両方へ足す。
- CI のテストは citest 経由で走り、落ちたテストを 1 回だけ再実行する。再実行で通ったテストは CI を落とさず、
  CI の完了後に `report-flaky-tests.yml` がテストごとに issue を起票する。再実行でも落ちれば CI は失敗する。
  `ci.yml` の `name:`、アップロードのステップ名、artifact の名前、profile は reporter との契約で、`make reporter-check` が突き合わせる。
- CI のランナーには本物の gh があり、git の利用者設定が無い。gh を使うテストは偽の gh を PATH の先頭に置き、`GH_TOKEN` を渡さない。
  git を使うテストは `GIT_CONFIG_GLOBAL=/dev/null` と `GIT_CONFIG_SYSTEM=/dev/null` で隔離し、作成者を `-c user.name=... -c user.email=...` で渡す。
- hhx 自身は hook の実行中にネットワークへ出ない。更新の確認は `hhx update` を明示的に実行したときだけ行う。
  GitHub の情報が要る hook（pr-context・pr-body-staleness）は、PATH 上の gh を子プロセスで起動して認証と通信を任せる。
  API を Go から直接呼ばない（認証を gh に任せ、テストで偽の gh に差し替えるため）。
- install のテストは一時的な HOME で行い、実機の設定ファイルに触れない。
- コメントは日本語で書き、保守に必要な意図・制約・契約だけを残す。
- 出力する文面（deny の理由文・注入するコンテキスト・CLI の出力）は、各パッケージの `messages.go` のカタログに英語と日本語で置く。
  - Go のソースの文字列リテラルに日本語を書くのは `messages.go` とテストだけにする（`cmd/hhx/catalog_test.go` が検査する）。
  - ID にはパッケージの利用者名（hook 名・`wait-ci`・`cli`）を前置きし、各 ID に文の意図（何を止め、何を促すか）を 1 行のコメントで書く。
  - 差し込みは `{{.Name}}` だけにし、英語と日本語で同じ名前の集合を使う（`i18n.Validate` が検査する）。
  - 翻訳しないもの: JSON のキー、注入のタグと項目名、`wait-ci:` の接頭辞・最終行・check の結果の語、利用者が設定で書いた文、
    discard-guard の snapshot のメッセージ（永続データなので規則の ID で書く）。
  - エージェント向けの英訳は、直訳ではなく同じ指示が伝わることを基準にする。「❌ Blocked:」「Reason:」「Action:」の段の数と順序を保ち、
    禁止と代替は命令文で書く（should / consider に弱めない）。コマンド・フラグ・パスは訳さない。
  - 表示言語は、hook では一次ゲートより後に `hookrt.Context.Language` で、CLI では設定から 1 回だけ決める。
    エラーの `Error()` は英語のままにし、表示するときに CLI がカタログの文面を選ぶ。
  - 日英対訳の一覧は `HHX_CATALOG_DUMP=<path> go test ./cmd/hhx -run TestDumpCatalogForReview` で書き出せる（見直し用。リポジトリには置かない）。

## hook の移植の型

Python 実装の hook は、`internal/hooks/prmergeguard` などの既存の移植と同じ形で移す。

- パッケージは `internal/hooks/<hook 名からハイフンを除いたもの>`。中身は次の 3 つにする。
  - `guard.go`: `Definition()`（名前・既定の有効・登録先・一次ゲート・`Run`）と判定のロジック。
    登録先は移植元の Claude の settings と Codex の hooks.json の matcher をそのまま写す。
  - `messages.go`: 理由文のカタログ（ID と英語・日本語の文面）。回避を思いとどまらせるのは理由文だけなので、迂回せず報告するよう文面で促す。
    迂回（同じ操作を分かりにくい書き方・別ツール・スクリプトファイル経由で行う）の禁止と、止めた操作を使わずに目的を達成する書き換えは確認なしで進めてよいことを書き分ける（書き分けないと、正当な書き換えまで止めてユーザーに尋ねる）。
    Claude Code と Codex の両方に出るので、片方にしか無いツール名を書かない。注入系の hook では、注入する文面と警告をここに置く。
    判定の関数は発火した規則の ID を返し、文面は出力の直前にカタログから組み立てる。
  - `guard_test.go`
- `internal/registry` の一覧へ、移植元の登録順の位置に足す。Makefile の `GO_COVERAGE_PACKAGES` にも足す。
- 判定は Python の意味を 1 対 1 で移す。書き直して「より正しく」しない。気付いた穴は README の Limits に書くか、別の作業に回す。
  - 例外として、移植元のままではデータを失う穴（discard-guard が別のリポジトリを保存して通すなど）は、1 対 1 よりデータを守ることを優先して直す。
    hhx に切り替えた後は Python 実装は動かないので、移植元に合わせて残す理由が無い。
    直した点はコードのコメントに移植元との違いとして書き、Go のテストで直した後の挙動を固定する。
    今ある例は discard-guard の 2 点（`( ... )` の中の cd を閉じ括弧で取り消す、1 つの git の複数の `-C` を順に適用する）。
    前者は、閉じ括弧で取り消した cwd に加え、括弧を無視して辿った cwd も保存する（どちらかが解決できなければ deny）。
    括弧の数え方はクォートや `case` のパターンを区別しないので、取り消しが誤っていても実際の破棄先を落とさないためである。
  - 一次ゲートのキーワードは変えない。「ゲートで抜ける＝判定しない」こともテストで固定されている。
    開錠の環境変数のように stdin の中身を見ずに抜ける条件も `Gate` に置く。
  - argv のデバッグ経路（`Context.FromArgs`）での引数の解釈は、移植元の hook ごとの扱いに合わせる。
    移植元が argv では一次ゲートを通さない（引数が payload でもコマンド文字列でもない）なら、`GateStdinOnly` を立てる。
  - Python で例外になって無出力で終わっていた入力（文字列でない `command` など）は、無出力にする。
- 正規表現と文字列処理の違いで判定がずれやすい。
  - `\s`・`\S`・`\d`・`\w`・`\W`・`\b`、`str.split()`・`strip()`・`splitlines()` は `internal/pycompat` を使う。Go の `\s`・`\w`・`\b` や `strings.Fields` は範囲が違う。
    `\b` の部品（`NotWordOrEnd`・`NotWordOrStart`）は 1 文字を消費するので、パターンの末尾か先頭にだけ置く。
    途中の `\b` は先読みと同じく同じ意味の形に展開し、展開の根拠をコメントに残す。
  - Python の `$` は末尾の改行の直前にも一致する。入力に改行が残るパターンは `\n?$` にし、改行つきのケースをテストに足す。
  - RE2 は先読み・後読みを扱えない。同じ意味の形に展開し、展開の根拠をコメントに残す。
  - 理由文に入力を JSON 文字列として埋め込むときは `pycompat.QuoteJSON` を使う（`encoding/json` は `<>&` と U+2028 をエスケープする）。
  - `os.path` のパス処理は `pycompat` の `Normpath`・`Realpath`・`Relpath` などを使う。`filepath.Clean` は先頭の `//` を畳み、
    `filepath.EvalSymlinks` は存在しないパスでエラーになり、`os.Getwd` は PWD 環境変数を返すことがある。
  - 先読み・後読みを手書きの走査に置き換えたときは、境界の断片（区切り文字・クォート・Unicode の空白や語の文字）を差し込んだケースを表に足す。
- テストは Go に移す。
  - 移植元の Python テストのケースは L2 も含めてすべて表の行として移す。L1 相当は `hooktest` で実運用と同じ経路（stdin の payload、argv）から起動し、判定と発火したルールのラベルを見る。
  - 奇妙な入力（空、`null`、`[]`、型の違う `command`・`tool_input`）、末尾の改行、設定で無効にしたときの無出力を足す。
  - 理由文や注入の中身は、期待する文字列を直書きせず、`hooktest.Language` の言語でカタログから引いて確かめる。
    `make test-ja`（CI の `test-ja` ジョブ）は同じテストを `HHX_TEST_LANGUAGE=ja` で流し、日本語の文面でも通ることを確かめる。
  - Codex の `additionalContextLimit` を持つ注入は、両言語の最長の文面が上限に収まることをテストする（英語は長くなりやすい）。
  - テストには実際の禁止語や個人のパスを書かず、架空の値（`acme-internal`、`/Users/alice`）を使う。
  - cwd が空のときやデバッグ経路でプロセスの作業ディレクトリを使う hook のテストは、`TestMain` で git 管理下でない一時ディレクトリへ移る
    （discard-guard はそこに snapshot を作ろうとするので、パッケージのディレクトリのままだと開発中のリポジトリに ref を作る）。
- 注入系の hook（判断を返さずコンテキストを足すもの）は `hookrt.Context` の `AddContext`・`Print`・`Notify` で出力する。
  - Go のテストは `hooktest.Output` で stdout を受け、`hooktest.ParseInjection` で判断のフィールドが無いことまで確かめる。
  - gh を呼ぶ hook のテストは `TestMain` で `hooktest.Main` を呼び、`hooktest.NewFakeGH` で偽の gh を PATH の先頭に置く。
    偽の gh の実体はテストのバイナリで、フィクスチャと呼び出しの記録の形は `internal/hooktest/fakegh.go` の規約に従う。
  - 状態は `internal/hookcache` の下に hook ごとのディレクトリを作って置く（0700 と 0600）。複数のプロセスが同じ記録を読み書きするなら、
    ロック用のファイルへの flock で排他する（agents-local-context）。
  - Python の `str()` と真偽の意味が要る値（tool_response の出力など）は `toolresponse.PyStr`・`toolresponse.Truthy` を使う。
