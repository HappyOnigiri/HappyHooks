// Package dangerousrmguard は、危険な rm / rmdir を、Claude Code が ask を出す前に deny する hook（dangerous-rm-guard）である。
//
// 背景:
//
// Claude Code は破滅的な削除を防ぐため、実行前にコマンド文字列を静的解析して確認ダイアログを出す
// （v2.1.239 の `V7r()` / `lal()` / `g9v()` とサーキットブレーカー `dangerousRemoval`）。
// この ask は bypassPermissions でも permission の allow ルールでも抑止できない（`bypassImmune: true`）ため、ユーザーの手が止まる。
// 一方 PreToolUse の hook の deny は権限エンジンより前に短絡する（`MTT()` の `if (e?.behavior === "deny") return ...`）。
// 同じ条件を先回りで deny すれば、ダイアログは出ず、Claude 側は理由文を受け取って次の手を決められる。
//
// 方針:
//   - 基本の判定は Claude Code v2.1.239 の組み込みのロジックを移植した。先行代入から派生した変数の削除は
//     v2.1.283 の判定を移した。その他の新しい分岐への追従は未確認である。
//   - 理由文は REWRITE と REPORT の 2 系統に分ける（messages.go）。REWRITE の案内には「ガードが働く形」だけを書く。
//     `"$BASE/x$name"` のように 1 文字挟んで正規表現を外す迂回を案内すると、安全性を上げずに検出だけ消えるので明示的に禁じる。
//
// 移植した分岐（組み込みのメッセージ → この hook の扱い）:
//
//	on possibly-empty variable path             → REWRITE  (A6V)
//	... via a derived variable                  → REWRITE  (v2.1.283 の e$t / i$t)
//	... inside command substitution             → REWRITE  (A6V を置換の中身にも適用)
//	too many command substitutions to analyze   → REWRITE  (64 個超)
//	on statically-unresolvable target           → REWRITE  (V7r の分岐 A / B / D)
//	on critical path                            → REPORT   (Kar)
//	on working directory or its ancestor        → REPORT   (MF。cwd と一致する場合だけ REWRITE)
//
// 組み込みは "statically-unresolvable" の 3 分岐に別々の理由文を持たせているので、こちらも分けて出す
// （cd で基準が不定 / 対象の形が上へ広がる / glob が複数階層）。1 本にまとめると、`cd tmp && rm -rf ./*` の正解である
// `rm -rf tmp/*` まで禁止しているように読めてしまう。コマンド置換の潰し方も組み込みに合わせて 2 通り使い分ける（flatten）。
//
// `$HOME` は expandKnownVars でホームディレクトリへ展開する。組み込みの変数解決 `Wr()` で実値を返す分岐を持つのは HOME だけ
// （`if(s==="HOME"){...return Am()}`、`Am` は os.homedir）なので、`rm -rf $HOME` は critical path として ask になる。
// 展開しないと hook 側にはリテラルのディレクトリ名に見えて素通りし、Kar の移植が片肺になる。
//
// `PWD` を同じに扱ってはいけない。`ni`（HOME / PWD / USER / PATH …）に入っている変数は
// 「センチネル `__TRACKED_VAR__` に置き換える」対象であって、実値に展開される対象ではない。
// `rm -rf $PWD` の対象は `<cwd>/__TRACKED_VAR__` に解決され、組み込みでは ask にならない。
//
// `${HOME}` の中括弧形は tree-sitter の `expansion` ノードになり、`Lt()` の switch に `case"expansion"` が無いため
// `ke()` の too-complex に落ちる。組み込みが ask を出すかは未確認。ホームを丸ごと消す形を素通りさせないため、
// 契約の例外として意図的に deny 側へ寄せている。
//
// 再代入は組み込み同様追わない。`export HOME=...` の後は実際には安全なコマンドまで deny になるが、
// 消したい実体をリテラルのパスで書けば通るので、打つ手はある。
//
// 移植していない条件（これらは今も ask のまま）:
//   - 追加作業ディレクトリ（`--add-dir`）とその祖先。PreToolUse の payload に一覧が無く、算出できない。cwd とその祖先だけを見る。
//   - 組み込みの `e_()` 相当のうち「削除対象が追跡できない変数展開を含む」側。`e_()` はトークナイザが置いた 2 種類の
//     sentinel（コマンド置換 / 変数展開）のどちらかを見ており、glob つきの対象に含まれていれば ask になる。
//     このうちコマンド置換の側は移植した（cmdsubToken を含む glob つき対象 → "cmdsub_glob"）。
//     変数展開の側は、組み込みが `HOME` / `PWD` などの既知変数や直前の `VAR=...` を追跡済みとして除外するのに対し、
//     hook からは追跡状態が見えない。`$` を含めば一律とすると `rm -rf "$HOME/.cache/foo/*"` まで止まるため入れない。
//     結果として、変数を含む対象はリテラルのディレクトリ名として解決される（組み込みより狭い側）。
//   - Windows 固有の判定（ドライブレター・UNC）。この hook は macOS 前提である。
//
// 外部コマンドは起動しない（realpath はシステムコールだけで行う）。
// デバッグ経路（`hhx hook dangerous-rm-guard '<コマンド>'`）では、cwd に依存する判定にプロセスの作業ディレクトリを使う。
// JSON として読めない入力は fail-open にせず、生の文字列をコマンドとして判定する。
package dangerousrmguard

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/HappyOnigiri/hhx/internal/hookrt"
	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

// Name は hook の名前である。
const Name = "dangerous-rm-guard"

// Definition は dangerous-rm-guard の定義を返す。組み込みの確認ダイアログを先回りするものなので、Claude にだけ登録する。
func Definition() hookrt.Definition {
	return hookrt.Definition{
		Name:           Name,
		DefaultEnabled: true,
		Registrations: []hookrt.Registration{
			{Agent: hookrt.Claude, Event: "PreToolUse", Matcher: "Bash"},
		},
		Gate: gate,
		Run:  run,
	}
}

// gate は一次ゲートである。組み込みの検査もすべて rm / rmdir が前提なので、rm を含まない入力は判定しない。
func gate(input []byte) bool {
	return strings.Contains(string(input), "rm")
}

func run(c *hookrt.Context) error {
	var command, cwd string
	var err error
	if c.FromArgs {
		command = string(c.Input)
		cwd, err = py.Getcwd()
	} else {
		command, cwd, err = payloadFrom(c.Input)
	}
	if err != nil {
		return err
	}
	if command == "" || !rmWordRE.MatchString(command) {
		return nil
	}
	found, err := evaluate(command, py.Normpath(cwd))
	if err != nil {
		return err
	}
	if found.target != "" {
		c.Deny(reason(c.Language(), found))
	}
	return nil
}

// payloadFrom は PreToolUse の payload からコマンドと cwd を取り出す。cwd が無ければプロセスの作業ディレクトリを使う。
// JSON の object として読めない入力は、fail-open にせず生の文字列のまま判定に回す。
// 文字列でない command は、移植元では判定の途中で例外になり無出力で終わっていたので、空として扱う。
func payloadFrom(raw []byte) (command, cwd string, err error) {
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil || payload == nil {
		cwd, err = py.Getcwd()
		return string(raw), cwd, err
	}
	toolInput, ok := payload["tool_input"].(map[string]any)
	if !ok {
		cwd, err = py.Getcwd()
		return "", cwd, err
	}
	cwd, _ = payload["cwd"].(string)
	if cwd == "" {
		if cwd, err = py.Getcwd(); err != nil {
			return "", "", err
		}
	}
	command, _ = toolInput["command"].(string)
	return command, cwd, nil
}

// evaluate は判定の本体で、拒否するなら発火した分岐（対象の表記・理由・対応のカタログの ID）を返す。
// 判定順は組み込みの V7r 内の順序に合わせる。分岐 A/B/D（静的解決不能）を分岐 C（重要ディレクトリ・作業ディレクトリ）より
// 先に見ないと、`cd sub && rm -rf ./*` が「作業ディレクトリごと」と誤って説明される。
func evaluate(command, cwd string) (finding, error) {
	if kind, token, resolved := findUnresolvableTarget(command, cwd); kind != "" {
		why, how := unresolvableReasons(kind)
		return finding{target: idUnresolvable, token: shown(token, resolved), why: why, how: how}, nil
	}
	// --- REPORT 系（対象の書き換えが迂回路にしかならない） ---
	kind, token, resolved, err := findProtectedPath(command, cwd)
	if err != nil {
		return finding{}, err
	}
	switch token := shown(token, resolved); kind {
	case "critical":
		return finding{target: idCritical, token: token, why: idWhyCritical, how: idHowCritical}, nil
	case "cwd":
		return finding{target: idCWD, token: token, why: idWhyCWD, how: idHowCWDItself}, nil
	case "workspace":
		return finding{target: idAncestor, token: token, why: idWhyAncestor, how: idHowAncestor}, nil
	}
	// --- REWRITE 系（安全な書き直しがある） ---
	if name, arg := findDangerousRemoval(command); name != "" {
		return finding{target: idEmptyVar, command: name, token: arg, why: idWhyEmptyVar, how: idHowEmptyVar}, nil
	}
	if name, arg := findDerivedVariableRemoval(command); name != "" {
		return finding{target: idEmptyVar, command: name, token: arg, why: idWhyDerived, how: idHowDerived}, nil
	}
	if name, arg := findInSubstitution(command); name != "" {
		return finding{target: idCmdsub, command: name, token: arg, why: idWhyCmdsub, how: idHowCmdsub}, nil
	}
	if len(substitutions(command)) > maxSubstitutions {
		return finding{target: idTooMany, why: idWhyTooMany, how: idHowTooMany}, nil
	}
	return finding{}, nil
}

func unresolvableReasons(kind string) (why, how string) {
	switch kind {
	case "cd":
		return idWhyCD, idHowCD
	case "cmdsub_glob":
		return idWhyCmdsubGlob, idHowCmdsubGlob
	case "shape":
		return idWhyShape, idHowShape
	default:
		return idWhyGlob, idHowGlob
	}
}

// shown は対象の表示である。`..` や `~` のように生のトークンだけでは何が消えるか分からない形は、解決後のパスを添える
// （組み込みは解決後のパスだけを出す）。内部で使う cmdsubToken は書いた覚えのない文字列なので、組み込みの vdi() と同じく
// `$(…)` に戻してから見せる。
func shown(token, resolved string) string {
	token = strings.ReplaceAll(token, cmdsubToken, "$(…)")
	resolved = strings.ReplaceAll(resolved, cmdsubToken, "$(…)")
	if resolved != "" && resolved != token {
		return token + " → " + resolved
	}
	return token
}

// --- Claude Code v2.1.239 からの移植 ---------------------------------------
//
// Python の \s・\S・\d は pycompat の文字クラスで書き、`\Z` は RE2 の `\z` にする。
// 先読み・後読みを使っていたもの（PAREN・BACKGROUND_AMP・KNOWN_VAR・PRIVATE_ALIAS）は、同じ置き換えをする走査関数にした。

const (
	// maxSubstitutions は組み込みが 1 コマンドあたりに許すコマンド置換の数である。
	maxSubstitutions = 64
	// cmdsubToken は実体パス判定（V7r 系）でコマンド置換を置き換えるトークンである。組み込みの g9v と同じ形。
	cmdsubToken = "__CMDSUB__"
	// assignments は先行する VAR=... 代入の並びである。
	assignments = `(?:[A-Za-z_][A-Za-z0-9_]*\+?=` + py.NotSpace + `*` + py.Space + `+)*`
)

var (
	// l6vRE はコマンド断片の先頭が rm / rmdir か（先行する VAR=... 代入とパス指定つき起動を許す）である。
	l6vRE = regexp.MustCompile(`^` + assignments + `\\?(?:[^=` + py.SpaceChars + `]*/)?(rm|rmdir)(?:` + py.Space + `|\z)`)
	// a6vRE は削除対象が `$VAR/` で始まり、直後が * $ / クォート 末尾 のいずれかかである。
	a6vRE = regexp.MustCompile(`^"?\$(?:\{[A-Za-z_][A-Za-z0-9_]*\}|[A-Za-z_][A-Za-z0-9_]*)"?/(?:\*|\$|/|["']|\z)`)
	// Claude Code v2.1.283 の e$t: 代入の右辺が空変数からルート直下へ広がる形。
	// 実装で使わない \uE020（引用済み区切りの番兵）は除いた。
	derivedRootRE = regexp.MustCompile(`^["']*\$(?:\{[A-Za-z_][A-Za-z0-9_]*` +
		`(?::?-(?:["']{2}|"?\$\{?[A-Za-z_][A-Za-z0-9_]*\}?"?)?)?\}|[A-Za-z_][A-Za-z0-9_]*)` +
		`["']*\\?/(?:[*?[{]|\$|/|["']|\z)`)
	// 同版の i$t: 単独の変数展開を削除対象にした形。
	bareVariableRE = regexp.MustCompile(`^["']*\$(?:\{([A-Za-z_][A-Za-z0-9_]*)` +
		`(?::?[?-][^}]*)?\}|([A-Za-z_][A-Za-z0-9_]*))["']*\z`)
	assignmentRE = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=([^` + py.SpaceChars + `;&|]+)(?:` + py.Space + `|\z)`)

	lineContinuationRE = regexp.MustCompile(`\\\r?\n`)
	backtickRE         = regexp.MustCompile("`([^`]*)`")
	segmentRE          = regexp.MustCompile(`[;|\n\r]|&&`)
	trailingClosersRE  = regexp.MustCompile(`[)\]}]+\z`)
	redirectHeadRE     = regexp.MustCompile(`^[` + py.Digit + `&]*[<>]`)
	redirectOnlyRE     = regexp.MustCompile(`^(?:[0-9]+|&)?(?:>>?[|&]?|<<?<?|<>)\z`)
	// rmWordRE は `\brm(?:dir)?\b` である。両端の \b は語の文字 r・m（r）の外側にあるので、1 文字を消費する形で書ける。
	rmWordRE  = regexp.MustCompile(py.NotWordOrStart + `rm(?:dir)?` + py.NotWordOrEnd)
	cdWordRE  = regexp.MustCompile(`^` + assignments + `(?:cd|chdir|pushd|popd)(?:` + py.Space + `|\z)`)
	globCharE = regexp.MustCompile(`[*?\[]`)
	// Python の $ は末尾の改行の直前にも一致するので、\n?\z で同じ意味にする。
	globTailRE      = regexp.MustCompile(`(?:/\*+)+/*(\n?)\z`)
	dotdotRE        = regexp.MustCompile(`(?:^|/)\.\.(?:/|\n?\z)`)
	trailingGlobDir = regexp.MustCompile(`\*/+\n?\z`)
	rmdirParentsRE  = regexp.MustCompile(`^--p|^-[a-z]*p`)
	privateAliasRE  = regexp.MustCompile(`(?i)^/private/(etc|var|tmp|home)`)
	slashesRE       = regexp.MustCompile(`/+`)
	whitespaceRE    = regexp.MustCompile(py.Space + `+`)
)

// alias は macOS の /private/{etc,var,tmp,home} を実効パスへ寄せる（組み込みの n() 相当）。
// 移植元の `(/|$)` の $ は末尾の改行の直前にも一致するので、直後が空・/・改行 1 文字のどれかなら寄せる。
func alias(path string) string {
	match := privateAliasRE.FindStringSubmatchIndex(path)
	if match == nil {
		return path
	}
	rest := path[match[1]:]
	if rest != "" && rest != "\n" && !strings.HasPrefix(rest, "/") {
		return path
	}
	return "/" + path[match[2]:match[3]] + rest
}

// realpath は移植元の _realpath である。OSError にあたる失敗は元のパスを返し、NUL を含むパス（ValueError）はエラーにする。
func realpath(path string) (string, error) {
	resolved, err := py.Realpath(path)
	if err == nil {
		return resolved, nil
	}
	if errors.Is(err, py.ErrNUL) {
		return "", err
	}
	return path, nil
}

// trimTrailingSlash は "/" 以外の末尾の / を 1 つ除く。
func trimTrailingSlash(path string) string {
	if path != "/" && strings.HasSuffix(path, "/") {
		return path[:len(path)-1]
	}
	return path
}

// isCriticalPath は組み込みの Kar() の移植である。ルート・最上位ディレクトリ・ホームディレクトリかを返す。
func isCriticalPath(path string) (bool, error) {
	collapsed := slashesRE.ReplaceAllString(path, "/")
	if collapsed == "*" || strings.HasSuffix(collapsed, "/*") {
		return true, nil
	}
	target := trimTrailingSlash(alias(collapsed))
	if target == "/" {
		return true, nil
	}
	home := trimTrailingSlash(alias(slashesRE.ReplaceAllString(py.Home(), "/")))
	if py.Lower(target) == py.Lower(home) {
		return true, nil
	}
	resolvedHome, err := realpath(py.Home())
	if err != nil {
		return false, err
	}
	// 移植元と異なり、ホームの実体にも対象と同じ別名処理を適用し、/private 経由の削除を見逃さない。
	canonical := trimTrailingSlash(alias(slashesRE.ReplaceAllString(resolvedHome, "/")))
	if canonical != home && py.Lower(target) == py.Lower(canonical) {
		return true, nil
	}
	return py.Dirname(target) == "/", nil
}

// removesWorkspace は組み込みの MF() の移植である。target が base と一致するか、base の祖先かを返す。
func removesWorkspace(base, target string) (bool, error) {
	relative, err := py.Relpath(py.Lower(alias(py.Normpath(base))), py.Lower(alias(py.Normpath(target))))
	if errors.Is(err, py.ErrEmptyPath) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch {
	case relative == ".":
		return true, nil
	case relative == ".." || strings.HasPrefix(relative, "../"):
		return false, nil
	default:
		return !py.IsAbs(relative), nil
	}
}

// escapesUpward は組み込みの F5n() の移植である。実体の要素より後ろに `..` が現れるかを返す。
func escapesUpward(path string) bool {
	seenReal := false
	for _, component := range strings.Split(path, "/") {
		switch component {
		case "", ".":
		case "..":
			if seenReal {
				return true
			}
		default:
			seenReal = true
		}
	}
	return false
}

// stripGlobTail は末尾の `/*` `/**` `/*/` … を取り除く（組み込みの for ループの移植）。
func stripGlobTail(path string) string {
	current := path
	for {
		previous := current
		stripped := globTailRE.ReplaceAllString(current, "${1}")
		if stripped == "" {
			stripped = "/"
		}
		if stripped != current {
			current = stripped
			if strings.Contains(stripped, "/") {
				current = py.Normpath(stripped)
			}
		}
		if previous == current {
			return current
		}
	}
}

// --- コマンド文字列の分解 ---------------------------------------------------

// expandKnownVars は `$HOME` と `${HOME}` をホームディレクトリへ展開する（移植元の KNOWN_VAR）。
// `$HOME` は直後が [A-Za-z0-9_] でないときだけ展開する（`$HOMEBREW_PREFIX` は展開しない）。
//
// trailingClosersRE を当てる前に呼ぶこと。`${HOME}` の `}` が閉じ括弧として落とされた後では、変数の形が崩れて一致しない。
// クォートの中でも展開するため、シェルなら展開されない `'$HOME'` も展開される。
// リテラルの `$HOME` という名前のディレクトリを消す形だけが誤爆で、実害のある側へは倒れない。
func expandKnownVars(token string) string {
	if !strings.Contains(token, "$") {
		return token
	}
	var out strings.Builder
	for index := 0; index < len(token); {
		rest := token[index:]
		switch {
		case strings.HasPrefix(rest, "${HOME}"):
			out.WriteString(py.Home())
			index += len("${HOME}")
		case strings.HasPrefix(rest, "$HOME") && !startsWithNameChar(rest[len("$HOME"):]):
			out.WriteString(py.Home())
			index += len("$HOME")
		default:
			out.WriteByte(token[index])
			index++
		}
	}
	return out.String()
}

func startsWithNameChar(text string) bool {
	if text == "" {
		return false
	}
	c := text[0]
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// unquote はトークンからクォートと `~` を取り除く（組み込みのトークナイザと XL() の近似）。
func unquote(token string) string {
	bare := strings.ReplaceAll(strings.ReplaceAll(token, `"`, ""), "'", "")
	if bare == "~" || strings.HasPrefix(bare, "~/") {
		return py.Home() + bare[1:]
	}
	return bare
}

// dropRedirects はリダイレクト指定を取り除く（組み込みでは AST が redirects として分離する）。
func dropRedirects(args []string) []string {
	var kept []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if redirectHeadRE.MatchString(arg) {
			if redirectOnlyRE.MatchString(arg) {
				index++ // `2> file` の file ごと読み飛ばす
			}
			continue
		}
		kept = append(kept, arg)
	}
	return kept
}

// positionalArgs は組み込みの fN() の移植である。最初の非オプション以降をすべて位置引数として扱う。
func positionalArgs(args []string) []string {
	var kept []string
	afterDashDash, started := false, false
	for _, arg := range args {
		switch {
		case afterDashDash || started:
			kept = append(kept, arg)
		case arg == "--":
			afterDashDash = true
		case arg == "-" || !strings.HasPrefix(arg, "-"):
			kept = append(kept, arg)
			started = true
		}
	}
	return kept
}

// replaceParens は移植元の PAREN（`(?<!\$)\([^()]*\)`）の置き換えである。
// 直前が $ でない ( から、( を挟まずに最初の ) までを replacement に置き換える。直前の文字は置き換える前の文字列で見る。
func replaceParens(text, replacement string) string {
	var out strings.Builder
	last := 0
	for index := 0; index < len(text); index++ {
		if text[index] != '(' || (index > 0 && text[index-1] == '$') {
			continue
		}
		closing := strings.IndexAny(text[index+1:], "()")
		if closing < 0 {
			break
		}
		if text[index+1+closing] == '(' {
			index += closing // 次の ( から探し直す
			continue
		}
		out.WriteString(text[last:index])
		out.WriteString(replacement)
		last = index + 1 + closing + 1
		index = last - 1
	}
	out.WriteString(text[last:])
	return out.String()
}

// cmdSubstMatches は移植元の CMD_SUBST（`\$\(([^()]*)\)`）の一致を、左から重ならないように返す。
// 各一致は [開始, 終わり) で、中身は括弧の内側である。括弧を何重にも潰すので、正規表現より速い走査にしてある。
func cmdSubstMatches(text string) [][2]int {
	var matches [][2]int
	for index := 0; index+1 < len(text); {
		if text[index] != '$' || text[index+1] != '(' {
			index++
			continue
		}
		closing := strings.IndexAny(text[index+2:], "()")
		if closing < 0 {
			break
		}
		end := index + 2 + closing
		if text[end] == '(' {
			// 間に ( が無いので、次の $( が始まり得るのは、この ( の直前の $ からである。
			index = end - 1
			continue
		}
		matches = append(matches, [2]int{index, end + 1})
		index = end + 1
	}
	return matches
}

// replaceCmdSubst は CMD_SUBST の一致をすべて replacement に置き換える。
func replaceCmdSubst(text, replacement string) string {
	matches := cmdSubstMatches(text)
	if len(matches) == 0 {
		return text
	}
	var out strings.Builder
	last := 0
	for _, match := range matches {
		out.WriteString(text[last:match[0]])
		out.WriteString(replacement)
		last = match[1]
	}
	out.WriteString(text[last:])
	return out.String()
}

// replaceBackgroundAmp は移植元の BACKGROUND_AMP（`(?<![<>&])&(?![<>&])`）の置き換えである。
// 前後がリダイレクトや && でない単独の & を ; にする。前後の文字は置き換える前の文字列で見る。
func replaceBackgroundAmp(text string) string {
	isRedirectOrAmp := func(c byte) bool { return c == '<' || c == '>' || c == '&' }
	out := []byte(text)
	for index := range len(text) {
		if text[index] != '&' {
			continue
		}
		if (index > 0 && isRedirectOrAmp(text[index-1])) || (index+1 < len(text) && isRedirectOrAmp(text[index+1])) {
			continue
		}
		out[index] = ';'
	}
	return string(out)
}

// flatten はコマンド置換と括弧を潰し、区切りで分割できる形にする。
//
// substitute はコマンド置換（`$(...)` / バッククォート）の置き換え先である。組み込みは走査ごとに使い分けており、こちらも揃える。
//   - " ": lal と同じ。A6V の走査（findDangerousRemoval）で使う。
//   - cmdsubToken: g9v と同じ。実体パスを解決する走査で使う。ここを空白にすると `rm -rf "$(pwd)/build"` が
//     `"` と `/build"` に割れ、`/build` を最上位ディレクトリの削除として誤検知する。
func flatten(command, substitute string) string {
	joined := lineContinuationRE.ReplaceAllLiteralString(command, " ")
	text := py.LStrip(backtickRE.ReplaceAllLiteralString(joined, substitute))
	for strings.HasPrefix(text, "(") || strings.HasPrefix(text, "{") {
		text = py.LStrip(text[1:])
	}
	for {
		previous := text
		text = replaceParens(replaceCmdSubst(text, substitute), " ")
		if text == previous {
			break
		}
	}
	return replaceBackgroundAmp(text)
}

// removal は rm / rmdir の断片 1 つである。
type removal struct {
	name string
	args []string
}

// removalSegments は rm / rmdir の断片を返す。
func removalSegments(command, substitute string) []removal {
	var found []removal
	for _, segment := range segmentRE.Split(flatten(command, substitute), -1) {
		head := py.LStrip(segment)
		match := l6vRE.FindStringSubmatchIndex(head)
		if match == nil {
			continue
		}
		name := "rm"
		if head[match[2]:match[3]] == "rmdir" {
			name = "rmdir"
		}
		found = append(found, removal{name: name, args: whitespaceRE.Split(head[match[1]:], -1)})
	}
	return found
}

// hasDirectoryChange は組み込みの J7r() 相当である。cd / pushd / popd / chdir を含むかを返す。
func hasDirectoryChange(command string) bool {
	for _, segment := range segmentRE.Split(flatten(command, cmdsubToken), -1) {
		if cdWordRE.MatchString(py.LStrip(segment)) {
			return true
		}
	}
	return false
}

// substitutions はコマンド置換の中身を集める（入れ子は内側から順に取り出す）。
func substitutions(command string) []string {
	var collected []string
	text := lineContinuationRE.ReplaceAllLiteralString(command, " ")
	for _, match := range backtickRE.FindAllStringSubmatch(text, -1) {
		collected = append(collected, match[1])
	}
	for {
		previous := text
		for _, match := range cmdSubstMatches(text) {
			collected = append(collected, text[match[0]+2:match[1]-1])
		}
		text = replaceCmdSubst(text, " ")
		if text == previous {
			return collected
		}
	}
}

// --- 判定 ------------------------------------------------------------------

// findDangerousRemoval は空になりうる変数を対象にした削除を見つけたら、コマンド名と対象を返す。
func findDangerousRemoval(command string) (name, target string) {
	if !strings.Contains(command, "$") || !rmWordRE.MatchString(command) {
		return "", ""
	}
	for _, segment := range removalSegments(command, " ") {
		for index := 0; index < len(segment.args); index++ {
			arg := trailingClosersRE.ReplaceAllLiteralString(segment.args[index], "")
			switch {
			case arg == "" || strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "'"):
			case redirectHeadRE.MatchString(arg):
				if redirectOnlyRE.MatchString(arg) {
					index++
				}
			case a6vRE.MatchString(arg):
				return segment.name, arg
			}
		}
	}
	return "", ""
}

// findDerivedVariableRemoval は、危険な形の右辺を代入した変数を後で丸ごと削除する形を見つける。
// v2.1.283 は D=$S/$n; rm -rf $D を、代入元の $S が空ならルート直下になるとして ask にする。
// 通常の rm -rf $DIR は従来どおり通し、代入の後の削除だけを対象にする。
func findDerivedVariableRemoval(command string) (name, target string) {
	derived := make(map[string]bool)
	for _, segment := range segmentRE.Split(flatten(command, " "), -1) {
		head := py.LStrip(segment)
		for _, keyword := range []string{"do", "then", "else"} {
			if strings.HasPrefix(head, keyword+" ") {
				head = py.LStrip(head[len(keyword):])
				break
			}
		}
		if match := assignmentRE.FindStringSubmatch(head); match != nil {
			derived[match[1]] = derivedRootRE.MatchString(match[2])
		}
		for _, removal := range removalSegments(head, " ") {
			for _, arg := range removal.args {
				match := bareVariableRE.FindStringSubmatch(arg)
				if match == nil {
					arg = trailingClosersRE.ReplaceAllLiteralString(arg, "")
					match = bareVariableRE.FindStringSubmatch(arg)
				}
				if match == nil {
					continue
				}
				variable := match[1]
				if variable == "" {
					variable = match[2]
				}
				if derived[variable] {
					return removal.name, arg
				}
			}
		}
	}
	return "", ""
}

// findInSubstitution はコマンド置換の中の危険な削除を見つけたら、コマンド名と対象を返す。
func findInSubstitution(command string) (name, target string) {
	if !strings.Contains(command, "$") && !strings.Contains(command, "`") {
		return "", ""
	}
	for _, inner := range substitutions(command) {
		if name, target := findDangerousRemoval(inner); name != "" {
			return name, target
		}
	}
	return "", ""
}

// resolvedTarget は削除対象 1 つを解決したものである。
type resolvedTarget struct {
	token string
	// target はクォートと ~・$HOME を解いたもの、absolute は cwd を基準にした絶対パス、stripped は末尾の glob を除いたもの。
	target, absolute, stripped string
	isRelative                 bool
}

// targets は断片の位置引数を解決して返す。空になった対象は除く。
func targets(segment removal, cwd string) []resolvedTarget {
	var nonEmpty []string
	for _, arg := range segment.args {
		if arg != "" {
			nonEmpty = append(nonEmpty, arg)
		}
	}
	var resolved []resolvedTarget
	for _, token := range positionalArgs(dropRedirects(nonEmpty)) {
		target := unquote(trailingClosersRE.ReplaceAllLiteralString(expandKnownVars(token), ""))
		if target == "" {
			continue
		}
		absolute := target
		if !py.IsAbs(target) {
			absolute = py.Normpath(py.Join(cwd, target))
		}
		resolved = append(resolved, resolvedTarget{
			token: token, target: target, absolute: absolute, stripped: stripGlobTail(absolute),
			isRelative: !py.IsAbs(target),
		})
	}
	return resolved
}

// remainder は相対指定の対象から cwd の部分を除いた残りである（絶対指定ならそのまま）。
func (t resolvedTarget) remainder(cwd string) string {
	if !t.isRelative {
		return t.stripped
	}
	prefix := cwd
	if !strings.HasSuffix(cwd, "/") {
		prefix += "/"
	}
	switch {
	case strings.HasPrefix(t.stripped, prefix):
		return t.stripped[len(prefix):]
	case t.stripped == cwd:
		return ""
	default:
		return t.stripped
	}
}

// findUnresolvableTarget は静的に解決できない削除対象を見つけたら、種別・対象・解決後を返す（V7r の分岐 A/B/D）。
// 種別は "cd" / "cmdsub_glob" / "shape" / "glob" のいずれかで、組み込みが分けている理由文に対応する。
func findUnresolvableTarget(command, cwd string) (kind, token, resolved string) {
	hasCD := hasDirectoryChange(command)
	for _, segment := range removalSegments(command, cmdsubToken) {
		for _, target := range targets(segment, cwd) {
			if kind, resolved := classifyUnresolvable(segment, target, cwd, hasCD); kind != "" {
				return kind, target.token, resolved
			}
		}
	}
	return "", "", ""
}

func classifyUnresolvable(segment removal, t resolvedTarget, cwd string, hasCD bool) (kind, resolved string) {
	if t.stripped == t.absolute {
		return "", ""
	}
	endsWithGlob := strings.HasSuffix(t.absolute, "/*")
	// 分岐 A: cd と相対 glob の併用。最終的な作業ディレクトリが確定しない。
	// 基準そのものが不明なので、解決後のパスは示さない（示すと嘘になる）。
	if hasCD && t.isRelative && endsWithGlob {
		return "cd", ""
	}
	// 分岐 B のうち `e_(p)` 相当。組み込みは「対象が置換または追跡できない変数展開を含む」で ask にする。
	// 置換は hook からも確実に見えるので、そちらだけ移植する（変数展開の側は追跡状態が見えず誤爆するため未移植）。
	if strings.Contains(t.target, cmdsubToken) {
		return "cmdsub_glob", ""
	}
	// 分岐 B: `..` や `~` を含む形、rmdir -p、`*/` で終わる相対指定。
	if escapesUpward(t.target) || strings.HasPrefix(t.target, "~") ||
		(t.isRelative && dotdotRE.MatchString(t.target) && endsWithGlob) ||
		(segment.name == "rmdir" && endsWithGlob && anyMatch(rmdirParentsRE, segment.args)) ||
		(t.isRelative && trailingGlobDir.MatchString(t.target) && endsWithGlob) {
		return "shape", t.absolute
	}
	// 分岐 D: glob が複数階層を跨ぐ（静的に列挙できない）。
	if endsWithGlob {
		depth := countComponents(t.absolute) - countComponents(t.stripped)
		globs := 0
		for _, component := range strings.Split(t.remainder(cwd), "/") {
			if globCharE.MatchString(component) {
				globs++
			}
		}
		if depth+globs > 1 {
			return "glob", t.absolute
		}
	}
	return "", ""
}

func anyMatch(pattern *regexp.Regexp, items []string) bool {
	for _, item := range items {
		if pattern.MatchString(item) {
			return true
		}
	}
	return false
}

// countComponents は / で区切った要素のうち、空と . を除いた数である。
func countComponents(path string) int {
	count := 0
	for _, component := range strings.Split(path, "/") {
		if component != "" && component != "." {
			count++
		}
	}
	return count
}

// findProtectedPath は重要ディレクトリ・作業ディレクトリの削除を見つけたら、種別・対象・解決後を返す。
// 種別は "critical" / "workspace" / "cwd" のいずれかである。
func findProtectedPath(command, cwd string) (kind, token, resolved string, err error) {
	bases := []string{cwd}
	resolvedCWD, err := realpath(cwd)
	if err != nil {
		return "", "", "", err
	}
	if resolvedCWD != cwd {
		bases = append(bases, resolvedCWD)
	}
	for _, segment := range removalSegments(command, cmdsubToken) {
		for _, target := range targets(segment, cwd) {
			// glob が残っている対象は、どのパスに当たるか確定しないので分岐 B/D の担当である。
			if target.stripped != target.absolute && globCharE.MatchString(target.remainder(cwd)) {
				continue
			}
			kind, candidate, err := classifyProtected(target.stripped, bases)
			if err != nil || kind != "" {
				return kind, target.token, candidate, err
			}
		}
	}
	return "", "", "", nil
}

func classifyProtected(stripped string, bases []string) (kind, candidate string, err error) {
	candidates := []string{stripped}
	resolved, err := realpath(stripped)
	if err != nil {
		return "", "", err
	}
	if resolved != stripped {
		candidates = append(candidates, resolved)
	}
	for _, candidate := range candidates {
		critical, err := isCriticalPath(candidate)
		if err != nil || critical {
			return "critical", candidate, err
		}
		for _, base := range bases {
			removes, err := removesWorkspace(base, candidate)
			if err != nil {
				return "", "", err
			}
			if !removes {
				continue
			}
			if py.Normpath(candidate) == py.Normpath(base) {
				return "cwd", candidate, nil
			}
			return "workspace", candidate, nil
		}
	}
	return "", "", nil
}
