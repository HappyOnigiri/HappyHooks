package dangerousrmguard

import "github.com/HappyOnigiri/hhx/internal/i18n"

// 理由文は 2 系統に分ける（パッケージの説明を参照）。
//   - REWRITE: 安全な書き直しがある分岐。書き直して再実行してよいと案内する（冒頭は「確認は不要」の 1 文）。
//     案内する書き直し先は、実際にこの hook を通ること（通らないなら通らないと書くこと）をテストで縛る。
//   - REPORT: 対象そのものが問題の分岐。削除せずに済む手段 → 範囲を狭める → ユーザーに判断を仰ぐ、の順に案内する
//     （冒頭は REWRITE と同じ「確認は不要」の 1 文に続けて「次の順に検討し、どうしても無理なときだけ作業を止める」）。
//     同じ範囲を別記法・別の作業ディレクトリで消す形は迂回になるので禁じる。
//
// 英語でも系統の違いが分かるよう、両系統とも "You do not need to ask the user before rewriting ..." で始め、
// REPORT はその後を "Work through the following in order, and stop only if ..." で揃える。
const (
	idReason = "dangerous-rm-guard.reason"
	idNoAsk  = "dangerous-rm-guard.no-ask"

	idHowEmptyVar   = "dangerous-rm-guard.how.empty-variable"
	idHowDerived    = "dangerous-rm-guard.how.derived-variable"
	idHowCD         = "dangerous-rm-guard.how.cd"
	idHowShape      = "dangerous-rm-guard.how.shape"
	idHowGlob       = "dangerous-rm-guard.how.glob"
	idHowCmdsub     = "dangerous-rm-guard.how.substitution"
	idHowTooMany    = "dangerous-rm-guard.how.too-many-substitutions"
	idHowCritical   = "dangerous-rm-guard.how.critical"
	idHowAncestor   = "dangerous-rm-guard.how.ancestor"
	idHowCWDItself  = "dangerous-rm-guard.how.cwd"
	idHowCmdsubGlob = "dangerous-rm-guard.how.substitution-glob"

	idWhyEmptyVar   = "dangerous-rm-guard.why.empty-variable"
	idWhyDerived    = "dangerous-rm-guard.why.derived-variable"
	idWhyCmdsub     = "dangerous-rm-guard.why.substitution"
	idWhyTooMany    = "dangerous-rm-guard.why.too-many-substitutions"
	idWhyCD         = "dangerous-rm-guard.why.cd"
	idWhyShape      = "dangerous-rm-guard.why.shape"
	idWhyGlob       = "dangerous-rm-guard.why.glob"
	idWhyCmdsubGlob = "dangerous-rm-guard.why.substitution-glob"
	idWhyCritical   = "dangerous-rm-guard.why.critical"
	idWhyCWD        = "dangerous-rm-guard.why.cwd"
	idWhyAncestor   = "dangerous-rm-guard.why.ancestor"

	// 発火した分岐ごとの対象の表記。テストはこれでどの分岐が発火したかを見る。
	idUnresolvable = "dangerous-rm-guard.target.unresolvable"
	idCritical     = "dangerous-rm-guard.target.critical"
	idCWD          = "dangerous-rm-guard.target.cwd"
	idAncestor     = "dangerous-rm-guard.target.ancestor"
	idEmptyVar     = "dangerous-rm-guard.target.empty-variable"
	idCmdsub       = "dangerous-rm-guard.target.substitution"
	idTooMany      = "dangerous-rm-guard.target.too-many-substitutions"
)

var messages = i18n.Register(i18n.Catalog{
	// 理由文の骨組み。対象・理由・対応の 3 段を、この順で出す。
	idReason: {
		EN: "❌ Blocked: {{.Target}}\n\nReason: {{.Why}}\n\nAction: {{.How}}",
		JA: "❌ ブロック: {{.Target}}\n\n理由: {{.Why}}\n\n対応: {{.How}}",
	},
	// 理由文の書き出し（REWRITE・REPORT の両系統）。書き直しての再実行はユーザーに確かめなくてよい。
	idNoAsk: {
		EN: "You do not need to ask the user before rewriting and running it again.",
		JA: "書き直して再実行するのにユーザーへの確認は不要です。",
	},

	// REWRITE: 空になりうる変数。リテラルの実体パスか `${BASE:?}` の形へ書き直させ、1 文字挟んで検出だけ外す形を禁じる。
	idHowEmptyVar: {
		EN: "{{.NoAsk}} Consider these in order and rewrite it.\n" +
			"  1. Write the target as a literal real path " +
			"(e.g. rm -rf /tmp/work/foo; to delete the contents at once, rm -rf /tmp/work/*)\n" +
			"  2. If you need a variable, use a form that rejects an empty value. Only a fixed name may follow it " +
			"(e.g. rm -rf \"${BASE:?}/$name\", rm -rf \"${BASE:?}\"/work); if a glob follows, as in `\"${BASE:?}\"/*`, " +
			"the `?` in `:?` counts as a glob character and the command is stopped again under " +
			"\"a deletion target that cannot be resolved statically\". To delete the contents at once, use 1\n" +
			"A form such as `\"$BASE/x$name\"` that only inserts one character to dodge the check is forbidden, " +
			"because the danger of an empty variable remains.\n" +
			"If there is a way that does not need the deletion at all, prefer it " +
			"(e.g. cleaning up a worktree only needs `git -C <main> worktree remove --force <path>`).",
		JA: "{{.NoAsk}}次の順に検討して書き直してください。\n" +
			"  1. 削除対象をリテラルの実体パスで書く " +
			"(例: rm -rf /tmp/work/foo、中身をまとめて消すなら rm -rf /tmp/work/*)\n" +
			"  2. 変数が要るなら空を弾く形にする。ただし続けてよいのは固定名までで " +
			"(例: rm -rf \"${BASE:?}/$name\"、rm -rf \"${BASE:?}\"/work)、" +
			"`\"${BASE:?}\"/*` のように glob を続けると `:?` の `?` が glob 文字として数えられ、" +
			"『静的に解決できない削除対象』側で改めて止まる。中身をまとめて消したいときは 1 を使う\n" +
			"`\"$BASE/x$name\"` のように 1 文字挟んで検出だけ外す書き方は、" +
			"空変数の危険がそのまま残るので禁止です。\n" +
			"そもそも削除が不要な手段があればそちらを優先してください " +
			"(例: worktree の後始末は `git -C <main> worktree remove --force <path>` で足ります)。",
	},
	// REWRITE: 危険な右辺から作った変数。削除対象側の空チェックでは防げない。
	idHowDerived: {
		EN: "{{.NoAsk}} Use a literal absolute path for the deletion target. " +
			"Checking the derived variable with `${VAR:?}` does not prevent its value from naming a top-level directory.",
		JA: "{{.NoAsk}}削除対象をリテラルの絶対パスで指定してください。" +
			"代入後の変数に `${VAR:?}` を付けても、最上位ディレクトリを指す値は防げません。",
	},
	// REWRITE: cd の後の相対 glob。cd を外して 1 つのパスで指させ、cd の位置だけを変える形を禁じる。
	idHowCD: {
		EN: "{{.NoAsk}} Drop the `cd` and specify the target as a single path.\n" +
			"  1. Remove the `cd` and point at the same range (e.g. `cd tmp && rm -rf ./*` → `rm -rf tmp/*`). " +
			"The range that is deleted does not change, and the target is fixed statically\n" +
			"  2. If you cannot write that, use an absolute path (e.g. rm -rf /abs/path/tmp/*)\n" +
			"Moving only the `cd` and keeping the relative glob is forbidden, because the base directory is still unknown.",
		JA: "{{.NoAsk}}`cd` を挟まず、削除対象を 1 つのパスで指定し直してください。\n" +
			"  1. `cd` を外して同じ範囲を指す (例: `cd tmp && rm -rf ./*` → `rm -rf tmp/*`)。" +
			"消える範囲は変わらず、対象は静的に確定します\n" +
			"  2. それが書けないなら絶対パスで指定する (例: rm -rf /abs/path/tmp/*)\n" +
			"`cd` の位置だけを変えて相対 glob を残す書き方は、基準が確定しないままなので禁止です。",
	},
	// REWRITE: 上の階層へ広がる形。`..` と `rmdir -p` を外し、当たりを確かめて実体パスで列挙させる。
	idHowShape: {
		EN: "{{.NoAsk}} Rewrite it so the target cannot spread to upper directories.\n" +
			"  1. Do not use `..`; write the real path you want to delete as an absolute path\n" +
			"  2. `rmdir -p` walks up and deletes parent directories that become empty, " +
			"so drop `-p` unless you mean that\n" +
			"  3. If there are several targets, check the matches with `ls -d <pattern>` and list them as real paths",
		JA: "{{.NoAsk}}削除対象が上の階層へ広がらない形に書き直してください。\n" +
			"  1. `..` を使わず、消したい実体を絶対パスで書く\n" +
			"  2. `rmdir -p` は空になった親ディレクトリまで遡って消すので、" +
			"そこまで意図していなければ `-p` を外す\n" +
			"  3. 対象が複数あるなら `ls -d <パターン>` で当たりを確認し、実体パスで列挙する",
	},
	// REWRITE: 複数階層の glob。当たりを確かめさせ、1 階層なら通ることを示し、確かめずに階層をずらす形を禁じる。
	idHowGlob: {
		EN: "{{.NoAsk}} Fix what it matches before deleting.\n" +
			"  1. Check what it matches with `ls -d <pattern>` or `find <dir> -maxdepth 1 ...`\n" +
			"  2. Replace the upper levels with real names. A glob in a single level passes " +
			"(e.g. `rm -rf tmp/*/cache` passes and `rm -rf tmp/*/*` does not), " +
			"so this is often enough\n" +
			"  3. If that still does not settle it, list what you checked as real paths and delete them " +
			"(e.g. rm -rf /tmp/work/a /tmp/work/b)\n" +
			"Shifting the levels only to dodge the check, without checking what it matches, is forbidden.",
		JA: "{{.NoAsk}}当たる先を確定させてから消してください。\n" +
			"  1. `ls -d <パターン>` か `find <dir> -maxdepth 1 ...` で何に当たるかを確認する\n" +
			"  2. 上位の階層を実体名に置き換える。glob が 1 階層だけなら通るので " +
			"(例: `rm -rf tmp/*/cache` は通り、`rm -rf tmp/*/*` は通らない)、" +
			"これだけで済むことが多い\n" +
			"  3. それでも決まらないなら、確認した結果を実体パスで列挙して削除する " +
			"(例: rm -rf /tmp/work/a /tmp/work/b)\n" +
			"当たる先を確認せずに階層をずらして検出だけ外す書き方は禁止です。",
	},
	// REWRITE: コマンド置換の中の削除。置換を単独で実行して確かめ、実体パスで消させる。
	idHowCmdsub: {
		EN: "{{.NoAsk}} Do not use the result of a command substitution directly as the deletion target.\n" +
			"  1. Run the substitution on its own and check the result\n" +
			"  2. List the result you checked as real paths and delete them\n" +
			"This is because nothing before execution can stop the target from turning into the root " +
			"when the substitution returns an empty string.",
		JA: "{{.NoAsk}}コマンド置換の結果を削除対象に直接使わないでください。\n" +
			"  1. 置換を単独で実行して結果を確認する\n" +
			"  2. 確認した結果を実体パスで列挙して削除する\n" +
			"置換が空文字列を返したときに削除対象がルートへ化けるのを、実行前に防げないためです。",
	},
	// REWRITE: 置換が多すぎる。削除を含む部分を分けて単独で実行させる。
	idHowTooMany: {
		EN: "{{.NoAsk}} Split the command and run the part with the deletion on its own. " +
			"There are too many command substitutions to check the deletion target before execution.",
		JA: "{{.NoAsk}}コマンドを分割して、削除を含む部分を単独で実行してください。" +
			"コマンド置換が多すぎて、削除対象を実行前に確認できません。",
	},
	// REPORT: システムの重要ディレクトリ。消さずに済むか → 範囲を狭める → 判断を仰ぐ、の順。別記法で同じ範囲を消す形を禁じる。
	idHowCritical: {
		EN: "{{.NoAsk}} Work through the following in order, and stop only if none of them works.\n" +
			"  1. Can you avoid deleting at all? If you use a variable, check its expansion with `echo`; " +
			"for a relative path, check `pwd` and the number of `..`. Most cases are an empty variable or a misspelled path, " +
			"and then there is nothing to delete\n" +
			"  2. Can you narrow the range? Specify only the real path you actually want to delete " +
			"(e.g. `/usr/local/share/mytool` instead of `/usr`)\n" +
			"  3. If neither works, stop, tell the user the purpose and the target, and ask for a decision\n" +
			"Deleting the same range with a different notation (e.g. `/usr` → `/usr/*`) is forbidden, because it only " +
			"dodges the check and deletes the same range.",
		JA: "{{.NoAsk}}次の順に検討し、どうしても無理なときだけ作業を止めてください。\n" +
			"  1. そもそも削除せずに済ませられないか — 変数を使っているなら `echo` で展開結果を、" +
			"相対パスなら `pwd` と `..` の数を確認する。空変数やパスの綴りの事故がほとんどで、" +
			"その場合は消す必要自体がありません\n" +
			"  2. 範囲を狭められないか — 本当に消したい実体だけを実体パスで指定し直す " +
			"(例: `/usr` ではなく `/usr/local/share/mytool`)\n" +
			"  3. どちらも無理なら、作業を止めて目的と対象をユーザーに伝え、判断を仰ぐ\n" +
			"同じ範囲を別記法で消す形 (例: `/usr` → `/usr/*`) は、判定を外すだけで消える範囲が" +
			"変わらないので禁止です。",
	},
	// REPORT: 作業ディレクトリの親。専用コマンド → 範囲を狭める → 判断を仰ぐ、の順。cd で移ってから消す形を禁じる。
	idHowAncestor: {
		EN: "{{.NoAsk}} Work through the following in order, and stop only if none of them works.\n" +
			"  1. Can you avoid deleting at all? To clean up a worktree or a temporary directory, " +
			"use the dedicated command " +
			"(e.g. `git -C <main> worktree remove --force <path>`; it succeeds even if the cwd is inside it)\n" +
			"  2. Can you narrow the range? If what you want to delete is under the working directory, " +
			"specify its real path again (it is often a wrong number of `..` or a misspelled path)\n" +
			"  3. If neither works, stop, tell the user the purpose and the target, and ask for a decision\n" +
			"Moving the working directory with `cd` and then deleting the same target is forbidden, because it only " +
			"dodges the check and deletes the same range.",
		JA: "{{.NoAsk}}次の順に検討し、どうしても無理なときだけ作業を止めてください。\n" +
			"  1. そもそも削除せずに済ませられないか — worktree や一時ディレクトリの後始末なら" +
			"専用コマンドを使う " +
			"(例: `git -C <main> worktree remove --force <path>`。cwd がその中にあっても成功します)\n" +
			"  2. 範囲を狭められないか — 消したいのが作業ディレクトリ配下なら、その実体パスを" +
			"指定し直す (`..` の数やパスの綴りの誤りであることが多い)\n" +
			"  3. どちらも無理なら、作業を止めて目的と対象をユーザーに伝え、判断を仰ぐ\n" +
			"`cd` で作業ディレクトリを移してから同じ対象を消す形は、判定を外すだけで消える範囲が" +
			"変わらないので禁止です。",
	},
	// REPORT: 作業ディレクトリ自身。専用コマンド → 実体パスで列挙して再実行 → 判断を仰ぐ、の順。
	idHowCWDItself: {
		EN: "{{.NoAsk}} Work through the following in order, and stop only if none of them works.\n" +
			"  1. Can a dedicated command do it? To clean up a worktree, use " +
			"`git -C <main> worktree remove --force <path>` (it succeeds even if the cwd is inside it); " +
			"for generated files under git, use `git clean -fd <path>`\n" +
			"  2. Can you narrow the range? If you only want to delete the contents, list the targets as real paths " +
			"and run it again (e.g. rm -rf ./build ./dist)\n" +
			"  3. If neither works, stop, tell the user the purpose and the target, and ask for a decision",
		JA: "{{.NoAsk}}次の順に検討し、どうしても無理なときだけ作業を止めてください。\n" +
			"  1. 専用コマンドで済ませられないか — worktree の後始末なら " +
			"`git -C <main> worktree remove --force <path>` (cwd がその中にあっても成功します)、" +
			"git 管理下の生成物なら `git clean -fd <path>`\n" +
			"  2. 範囲を狭められないか — 中身だけ消したいなら、消す対象を実体パスで列挙して再実行する " +
			"(例: rm -rf ./build ./dist)\n" +
			"  3. どちらも無理なら、作業を止めて目的と対象をユーザーに伝え、判断を仰ぐ",
	},
	// REWRITE: 置換 + 末尾の glob。置換を単独で確かめて実体パスに glob を付けさせる。置換を残す形は迂回にならないと伝える。
	idHowCmdsubGlob: {
		EN: "{{.NoAsk}} Check the result of the substitution, then delete with a real path.\n" +
			"  1. Run only the substitution on its own and check the result " +
			"(e.g. `pwd`, `git rev-parse --show-toplevel`)\n" +
			"  2. Write the result you checked as a real path and put the glob under it (e.g. rm -rf /abs/path/tmp/*)\n" +
			"Keeping the substitution and adding a glob is not a way around this: even if this hook lets it through, " +
			"the built-in check turns it into a confirmation dialog.",
		JA: "{{.NoAsk}}置換の結果を確かめてから、実体パスで消してください。\n" +
			"  1. 置換だけを単独で実行して結果を確認する " +
			"(例: `pwd`、`git rev-parse --show-toplevel`)\n" +
			"  2. 確認した結果を実体パスで書き、glob はその下に付ける (例: rm -rf /abs/path/tmp/*)\n" +
			"置換を残したまま glob を付ける形は、このフックを通しても組み込み側で" +
			"確認ダイアログになるので、迂回になりません。",
	},

	// 以下は各分岐の理由。なぜ静的に安全と判定できないか、または消すと何が起きるかを書く。
	idWhyEmptyVar: {
		EN: "The target starts with `$variable/`, so when the variable is undefined or empty the target turns into " +
			"the filesystem root (or a top-level directory). " +
			"The variable's value cannot be known before execution, so it cannot be judged safe statically.",
		JA: "削除対象が `$変数/` で始まっており、変数が未定義・空のときに削除対象が " +
			"ファイルシステムのルート (や最上位ディレクトリ) に化けます。" +
			"変数の中身は実行前に確定できないため、静的には安全と判定できません。",
	},
	// 危険な右辺を経由した変数は、値が空でなくてもルート直下を指し得る。
	idWhyDerived: {
		EN: "The target is a variable assigned from a path beginning with another variable and `/`. " +
			"If that source variable is empty, the assigned value can name a top-level directory.",
		JA: "削除対象の変数には、別の変数と `/` で始まるパスが代入されています。" +
			"代入元の変数が空なら、代入後の値は最上位ディレクトリを指し得ます。",
	},
	idWhyCmdsub: {
		EN: "A command substitution contains a deletion whose target is a variable that may be empty. " +
			"The result of the substitution cannot be known before execution, so it cannot be judged safe statically.",
		JA: "コマンド置換の中に、空になりうる変数を対象にした削除があります。" +
			"置換の結果は実行前に確定できないため、静的には安全と判定できません。",
	},
	idWhyTooMany: {
		EN: "There are too many command substitutions to analyze the deletion target statically.",
		JA: "コマンド置換が多すぎて、削除対象を静的に解析できません。",
	},
	idWhyCD: {
		EN: "There is a `cd` before the deletion, so which working directory the relative glob expands against " +
			"cannot be known before execution.",
		JA: "削除の前に `cd` があるため、相対 glob がどの作業ディレクトリを基準に展開されるかを" +
			"実行前に確定できません。",
	},
	idWhyShape: {
		EN: "The target contains a glob and also crosses `..`, walks up parents with `rmdir -p`, or ends with `*/`. " +
			"How far up directories are deleted cannot be known before execution.",
		JA: "削除対象が glob を含み、かつ `..` を跨ぐ・`rmdir -p` で親を遡る・`*/` で終わる、" +
			"といった形になっています。どのディレクトリまで消えるかを実行前に確定できません。",
	},
	idWhyGlob: {
		EN: "The glob spans several levels (e.g. `tmp/*/*`), so the paths it matches cannot be listed statically. " +
			"The `?` in a form such as `${VAR:?}` also counts as a glob character.",
		JA: "glob が複数の階層に跨がっていて (例: `tmp/*/*`)、どのパスに当たるかを" +
			"静的に列挙できません。`${VAR:?}` のような書き方に含まれる `?` も " +
			"glob 文字として数えます。",
	},
	idWhyCmdsubGlob: {
		EN: "The target contains a command substitution and ends with a glob. " +
			"The result of the substitution is unknown before execution, so what the glob matches is unknown too.",
		JA: "削除対象がコマンド置換を含み、かつ glob で終わっています。" +
			"置換の結果が実行前に確定しないので、glob がどこに当たるかも確定できません。",
	},
	idWhyCritical: {
		EN: "This deletes a critical system directory (the root, a top-level directory, or the home directory). " +
			"Running it would make recovering the machine very costly.",
		JA: "システムの重要ディレクトリ (ルート・最上位ディレクトリ・ホームディレクトリ) " +
			"の削除です。実行するとマシンの復旧に大きなコストがかかります。",
	},
	idWhyCWD: {
		EN: "The target resolves to the working directory itself (a form that points at its contents, such as `./*`, " +
			"also matches the working directory once the glob is removed). Running it would delete the ground the session " +
			"stands on, or, even if it does not, leave the base of later commands undefined.",
		JA: "削除対象が作業ディレクトリ自身に解決されます (`./*` のように中身を指す形も、" +
			"glob を外すと作業ディレクトリに一致します)。実行するとセッションの足元が消えるか、" +
			"消えなくても以降のコマンドの基準が不定になります。",
	},
	idWhyAncestor: {
		EN: "This deletes a parent of the working directory. " +
			"Running it would delete the ground the session stands on along with it.",
		JA: "作業ディレクトリの親ディレクトリの削除です。" +
			"実行するとセッションの足元ごと消えます。",
	},

	// 対象の表記。{{.Command}} は rm か rmdir である。
	idUnresolvable: {EN: "a deletion target that cannot be resolved statically", JA: "静的に解決できない削除対象"},
	idCritical:     {EN: "deleting a critical system directory", JA: "システム重要ディレクトリの削除"},
	idCWD:          {EN: "the working directory itself is the deletion target", JA: "作業ディレクトリ自身が削除対象"},
	idAncestor:     {EN: "deleting a parent of the working directory", JA: "作業ディレクトリの親の削除"},
	idEmptyVar:     {EN: "{{.Command}} on a variable that may be empty", JA: "空になりうる変数を対象にした {{.Command}}"},
	idCmdsub:       {EN: "dangerous {{.Command}} inside a command substitution", JA: "コマンド置換の中の危険な {{.Command}}"},
	idTooMany:      {EN: "a deletion command with too many command substitutions", JA: "コマンド置換が多すぎる削除コマンド"},
})

// finding は発火した分岐である。target・why・how はカタログの ID で、command は rm か rmdir、
// token は対象の表記の後ろに括弧で添えるトークン（空なら添えない）である。
type finding struct {
	target  string
	command string
	token   string
	why     string
	how     string
}

func reason(language i18n.Language, found finding) string {
	target := messages.Text(language, found.target, map[string]any{"Command": found.command})
	if found.token != "" {
		target += " (" + found.token + ")"
	}
	noAsk := messages.T(language, idNoAsk)
	return messages.Text(language, idReason, map[string]any{
		"Target": target,
		"Why":    messages.T(language, found.why),
		"How":    messages.Text(language, found.how, map[string]any{"NoAsk": noAsk}),
	})
}
