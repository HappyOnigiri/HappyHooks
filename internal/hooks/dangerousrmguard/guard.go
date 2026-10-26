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
//     v2.1.283 の判定を、shell -c のスクリプトの検査は v2.1.296 の判定を、読み切れない `${…}` と `$(…)` の検査は
//     v2.1.309 の判定を移した（ファイル末尾の節）。
//     その他の新しい分岐への追従は未確認である。
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
//	shell -c script ... could not be checked    → REWRITE  (v2.1.296 の Fcn / Lwe)
//	in a shell -c script, on a target built ... → REWRITE  (v2.1.296 の HTr。中身は上の各分岐で判定する)
//	the shell may read it differently ...       → REWRITE  (v2.1.309 の bKe。`${` の直後を読み切れない形と、`$(…)` の中の `))`)
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
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

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

// gate は一次ゲートである。rm を含む入力、シェルの -c らしき入力（`sh` と `-…c` の両方を含む）、`${` を含む入力、
// `$(` と `))` の両方を含む入力を判定する。
// 後の 3 つは、v2.1.296 の shell -c スクリプトの検査と v2.1.309 の読み切れない形の検査が、
// rm の語の無いコマンドでも ask を出すためである。
func gate(input []byte) bool {
	text := string(input)
	return strings.Contains(text, "rm") || (strings.Contains(text, "sh") && inlineGateRE.MatchString(text)) ||
		strings.Contains(text, "${") || (strings.Contains(text, "$(") && strings.Contains(text, "))"))
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
	if command == "" {
		return nil
	}
	found, err := evaluateAt(command, py.Normpath(cwd), 0)
	if err != nil {
		return err
	}
	if found.target == "" {
		// 組み込みは rm の分岐を先に見て、どれにも当たらなければこの ask を出す。最上位のコマンドだけが対象である。
		found = findUnparsed(command)
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

// evaluateAt は depth 段目の shell -c の中のコマンドを判定する。rm の分岐を先に見て、無ければ shell -c の分岐を見る。
func evaluateAt(command, cwd string, depth int) (finding, error) {
	if rmWordRE.MatchString(command) {
		if found, err := evaluate(command, cwd); err != nil || found.target != "" {
			return found, err
		}
	}
	return findInlineShell(command, cwd, depth)
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
	rmWordRE = regexp.MustCompile(py.NotWordOrStart + `rm(?:dir)?` + py.NotWordOrEnd)
	// leadingKeywordsRE は断片の先頭に続く予約語の並びである。`do ! rm` のように重なる形も外す。
	leadingKeywordsRE = regexp.MustCompile(`^(?:(?:do|then|else|elif|if|while|until|coproc|!|\{)` + py.Space + `+)+`)
	cdWordRE          = regexp.MustCompile(`^` + assignments + `(?:cd|chdir|pushd|popd)(?:` + py.Space + `|\z)`)
	globCharE         = regexp.MustCompile(`[*?\[]`)
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
		head := stripLeadingKeywords(py.LStrip(segment))
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

// stripLeadingKeywords は断片の先頭の制御構文の予約語（`do` `then` `if` `!` `{` など）を外す。
// 移植元は外さず、`for v in a b; do rm -rf $S/$v; done` を素通りさせていた。組み込みは tree-sitter の AST で
// 予約語の外にある rm を見るので ask になり、bypass でもユーザーの手が止まる。ここで外して先回りの deny を揃える。
func stripLeadingKeywords(head string) string {
	if match := leadingKeywordsRE.FindStringIndex(head); match != nil {
		return head[match[1]:]
	}
	return head
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
		head := stripLeadingKeywords(py.LStrip(segment))
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

// --- Claude Code v2.1.296 の shell -c スクリプトの検査からの移植 ---------------------
//
// v2.1.296 は `bash -c '<スクリプト>'` のスクリプトを取り出して（`FTr`）削除を検査する（`Fcn`）。
//   - スクリプトを静的に検査できず、かつ実行時に rm になりうる語がコマンドの位置にあると、rm の語が無くても ask にする
//     （`Lwe`。"This command passes a shell -c script that runs rm, and Claude Code could not check the script ..."）。
//   - 検査できるなら、位置引数を実値か `${__INLINE_SHELL_VALUE__}` に置き換えたスクリプトを再帰的に検査する。
//     対象が置き換えた値から作られていれば、理由文を「実行時の値を対象にした shell -c の中の rm」に差し替える（`HTr`）。
//
// どちらも JS の正規表現で書かれているので、JS の意味（`\s` は Unicode の空白、`\w`・`\d`・`\b` は ASCII、
// `.` は改行類を除く 1 文字）で移す。py.Space などの Python の文字クラスは使わない。
//
// 移植していない条件（組み込みでは ask になり得るが、ここでは通す）:
//   - tree-sitter に依存する「検査しきれない」の原因（再帰検査での too-complex、検査の予算切れ、`homeWritten`、
//     スクリプトの中のホームディレクトリのリテラル）と、permission の deny ルールに当たる場合（`Ze`）。
//   - スクリプトの前に置かれる `cd`・環境変数の受け渡し。外側に cd があるときは再帰検査をしない。
//   - コマンドの前置きは代表的なもの（時間・優先度・環境・権限の wrapper）だけを外す。`xargs` などの後ろのスクリプトは見ない。
//
// 組み込みとずれ得る点: 組み込みはこの検査を機能フラグ（`wX()`。既定で有効、サーバーから無効にできる）でも切り替える。
// hook からはフラグが見えないので、無効にされた環境ではこちらだけが止める。環境変数 disableInlineEnv は両方を止める。

const (
	// inlineShellValue は実行時にしか分からない値の置き換え先である（組み込みの `_w`）。
	inlineShellValue = "__INLINE_SHELL_VALUE__"
	// inlineShellGuarded は `${1:?}` の形で守られた空の値の置き換え先である。
	inlineShellGuarded = "${__INLINE_SHELL_GUARDED__:?}"
	// maxInlineScript を超える長さのスクリプトは検査しない（組み込みの `Qcn`。JS の文字列長で数える）。
	maxInlineScript = 2000
	// maxInlineDirectoryChanges を超える数の cd を含むスクリプトは検査しない（組み込みの `LTr`）。
	maxInlineDirectoryChanges = 8
	// maxInlineDepth は入れ子の shell -c を検査する深さの上限である（組み込みの `i3e`）。
	maxInlineDepth = 2
	// disableInlineEnv は組み込みのこの検査を切る環境変数である。設定されていれば組み込みも ask を出さないので、こちらも止めない。
	disableInlineEnv = "CLAUDE_CODE_DISABLE_INLINE_SHELL_RM_PROMPT"

	// jsSpaceChars は JS の `\s` の文字である。
	jsSpaceChars = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`
	jsSpace      = `[` + jsSpaceChars + `]`
	jsNotSpace   = `[^` + jsSpaceChars + `]`
	// jsDot は JS の `.`（改行類を除く 1 文字）である。
	jsDot = `[^\n\r\x{2028}\x{2029}]`
	// outerPunctuation は外側のコマンドを単純コマンドに分ける区切りの文字である。
	outerPunctuation = ";&|()<>\n"
)

var (
	// inlineGateRE はシェルの -c らしきオプションである（一次ゲートで使う）。
	inlineGateRE = regexp.MustCompile(`-[A-Za-z]*c`)

	// u$ の部品。
	usQuotedRE       = regexp.MustCompile(`\\(?s:.)|'[^']*'|"(?:[^"\\]|\\` + jsDot + `)*"`)
	usQuotedCharsRE  = regexp.MustCompile(`[` + jsSpaceChars + ";&|()`{}!" + `]`)
	usDupRE          = regexp.MustCompile(`\d*[<>]&(?:\d+-?|-)`)
	usRedirectRE     = regexp.MustCompile(`\d*[<>]&|&>>?|>\|`)
	usStripQuotesRE  = regexp.MustCompile(`\$['"]|['"\\]`)
	usSplitRE        = regexp.MustCompile("[;&|()`{}\\n!]|\\$\\(")
	usWordSplitRE    = regexp.MustCompile(`[ \t]+`)
	usRedirectWordRE = regexp.MustCompile(`^\d*[<>]`)
	usRedirectOnlyRE = regexp.MustCompile(`^\d*[<>]+$`)
	usAssignmentRE   = regexp.MustCompile(`^\w+\+?=`)

	// Dwe の部品。
	removalWordRE   = regexp.MustCompile(`(?i)^(?:\w*=)?rm(?:dir)?$`)
	removalListRE   = regexp.MustCompile(`(?i)(?:^|,)rm(?:dir)?(?:,|\$)`)
	removalAssignRE = regexp.MustCompile(`(?i)^\w+=(?:` + jsNotSpace + `*/)?rm(?:dir)?$`)
	redirectTailRE  = regexp.MustCompile(`(?s)\d*[<>].*$`)
	findExecRE      = regexp.MustCompile(`^-(?:exec(?:dir)?|ok(?:dir)?)$`)
	shortFlagsRE    = regexp.MustCompile(`^-[a-zA-Z]+$`)

	// Zcn・NTr・efn の部品。
	zcnRE         = regexp.MustCompile(`(?:[^` + jsSpaceChars + `'"\\]|\\(?s:.)|'[^']*'|"(?:[^"\\]|\\` + jsDot + `)*")+`)
	braceSplitRE  = regexp.MustCompile(`,|\.\.`)
	braceShapeRE  = regexp.MustCompile(`(?s)^([^{]*)\{([^{}]*)\}(.*)$`)
	braceVarRE    = regexp.MustCompile(`\$\{[^{}]*\}`)
	braceHeadRE   = regexp.MustCompile(`^["']?[/~$]|[{},\\'"]|\.\.`)
	braceTailRE   = regexp.MustCompile(`[~{},\\'"]`)
	braceItemRE   = regexp.MustCompile(`[/~$*?\[]|^\.\.?$|\brm`)
	braceEmptyRE  = regexp.MustCompile(`[$]|^["']?/`)
	quoteCharsRE  = regexp.MustCompile(`["'\\]`)
	removalPathRE = regexp.MustCompile(`(?i)(?:^|/)rm(?:dir)?$`)

	// FTr の部品。
	aliasRE   = regexp.MustCompile(`\balias\b`)
	ifsRE     = regexp.MustCompile(`\bIFS\b`)
	ifsReadRE = regexp.MustCompile(`(?:^|[;&|\n({]` + jsSpace + `*)(?:(?:do|then|else|elif|if|while|until)` + jsSpace +
		`+)?IFS=(?:'[^']*'|"[^"]*"|[^` + jsSpaceChars + `;&|'"\\])*[ \t]+read\b`)
	// 移植元の `=(?!=)\S` は「= の後が = でない空白以外の 1 文字」なので、否定の文字クラスに展開した。
	zshRE = regexp.MustCompile(`(?:^|[` + jsSpaceChars + `;&|(){}])=[^=` + jsSpaceChars +
		`]|\$\{?\w+\}?(?::[htrelquQxsaAcP&]|\[)|\bargv\b|\$(?:[=~^]|\{[=~^(])|\(:`)
	positionalUseRE  = regexp.MustCompile(`\$(?:\{[!#@*0-9]|[#@*0-9])`)
	positionalLookRE = regexp.MustCompile("^(?:[0-9]+(?:\\}|:?\\?[^}$`]*\\})|[@*#]\\})")
	positionalRE     = regexp.MustCompile(`\\(?s:.)|"\$\{?@\}?"|\$\{([0-9]+)(:?)\?[^}]*\}|` +
		`\$(?:\{([0-9]+|[@*#])\}|([0-9@*#]))`)
	escapeRE       = regexp.MustCompile(`\\(?s:.)`)
	literalArgRE   = regexp.MustCompile(`^[\w./@%+=:,*?[\]!^{} \t\n-]*$`)
	tildeRE        = regexp.MustCompile(`^~(?:/|$)`)
	shellVersionRE = regexp.MustCompile(`-\d+(?:\.\d+)*$`)
	shellBashRE    = regexp.MustCompile(`^(?:r?ba)?sh$`)
	shellKshRE     = regexp.MustCompile(`ksh$`)
	shellLongRE    = regexp.MustCompile(`^--?(?:login|posix|noprofile|norc|noediting|verbose|restricted|debugger|` +
		`dump-(?:po-)?strings|wordexp|pretty-print|help|version|(rcfile|init-file))$`)
	shellOptionRE    = regexp.MustCompile(`^[-+].`)
	shellBadFlagsRE  = regexp.MustCompile(`[^abcefhiklmnoprstuvxBCDEHIOPT]`)
	shellKshFlagRE   = regexp.MustCompile(`[oO].`)
	shellOptionArgRE = regexp.MustCompile(`[oO]`)
	shellCommandFlag = regexp.MustCompile(`^-[A-Za-z]*c`)

	// Ncn の部品。
	ncnRE = regexp.MustCompile(`\bfunction\b|\balias\b|\bIFS\b|\w` + jsSpace + `*\(` + jsSpace + `*\)|` +
		`(?:^|[` + jsSpaceChars + `;&|(){}])(?:argv|[0-9]+)(?:\[[^\]]*\])?=|` +
		`(?:^|[` + jsSpaceChars + `;&|(){}!])set` + jsSpace + `[^;&|\n]*(?:\$\(|` + "`)")
	ncnBuiltinRE   = regexp.MustCompile(`^(?:shift|source|\.|eval|trap)$`)
	ncnDashRE      = regexp.MustCompile(`^(?:-|\d+$)`)
	ncnFlagRE      = regexp.MustCompile(`^[-+]`)
	ncnOptionArgRE = regexp.MustCompile(`[oO]$`)

	// Jcn の部品。移植元の `rm(?:dir)?(?![\w.-])` は、直後の 1 文字を消費する形に展開した（パターンの末尾なので意味は同じ）。
	jcnBraceRE    = regexp.MustCompile(`\{[^{}` + jsSpaceChars + `]*(?:,|\.\.)`)
	jcnRemovalRE  = regexp.MustCompile(`(?i)(?:^|[^\w.-])rm(?:dir)?(?:[^\w.-]|$)`)
	jcnVariableRE = regexp.MustCompile("(?:^|[;&|(){}\\n`!])" + jsSpace +
		`*(?:\w+\+?=` + jsNotSpace + `*` + jsSpace + `+)*\$`)

	directoryChangeRE = regexp.MustCompile(`^(?:cd|pushd|popd|chdir)$`)
	nameAssignmentRE  = regexp.MustCompile(`^[A-Za-z_]\w*\+?=`)
	exeSuffixRE       = regexp.MustCompile(`(?i)\.exe$`)
	jsSpaceRE         = regexp.MustCompile(`^` + jsSpace + `$`)

	// shells は組み込みの `rde` である。
	shells = setOf("bash", "sh", "zsh", "dash", "ksh", "mksh", "ash", "hush", "rbash")
	// commandWrappers は組み込みの `B3e` である（後ろに別のコマンドを取る語と、制御構文の予約語）。
	commandWrappers = setOf("sudo", "doas", "exec", "command", "env", "nice", "nohup", "busybox", "time", "timeout",
		"stdbuf", "setsid", "ionice", "pkexec", "runuser", "eval", "xargs",
		"do", "then", "else", "elif", "if", "while", "until", "!", "{", "coproc",
		"builtin", "noglob", "toybox", "trap", "watch", "flock", "chroot", "script")
	// keywords は単純コマンドの先頭から外す予約語である（tree-sitter はこれらを command の外に置く）。
	keywords = setOf("do", "then", "else", "elif", "if", "while", "until", "!", "{", "}", "coproc")
)

func setOf(items ...string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[item] = true
	}
	return set
}

// inlineScript は `FTr` の結果である。judgedOK が false なら、組み込みはスクリプトを検査できない。
type inlineScript struct {
	script   string
	judged   string
	judgedOK bool
}

// jsLength は JS の文字列長（UTF-16 の符号単位の数）である。
func jsLength(text string) int {
	length := 0
	for _, r := range text {
		length++
		if r > 0xFFFF {
			length++
		}
	}
	return length
}

func isJSSpace(r rune) bool {
	return jsSpaceRE.MatchString(string(r))
}

// baseName は組み込みの `TR` である。パスの最後の要素から `.exe` を除く。
func baseName(word string) string {
	if index := strings.LastIndexAny(word, `\/`); index >= 0 {
		word = word[index+1:]
	}
	return exeSuffixRE.ReplaceAllLiteralString(word, "")
}

// shellName は組み込みの `$ke` である。`bash-5.2` のような版の付いた名前も同じシェルとみなす。
func shellName(word string) string {
	return shellVersionRE.ReplaceAllLiteralString(strings.ToLower(baseName(word)), "")
}

// simpleCommands は外側のコマンドを単純コマンドの argv に分ける（組み込みは tree-sitter の command ノードの argv を見る）。
// リダイレクトは演算子と続くファイル名を除く。クォートが閉じないなど字句解析できない入力は判定しない（nil）。
// heredoc を含む入力も判定しない。本文の行をコマンドとして読んでしまい、スクリプトを書き出すだけの
// `cat > x.sh <<'EOF'` まで止めるためである（組み込みは本文をコマンドとして扱わない）。
// コメントやコマンド置換の中までは追わない（組み込みより狭い側）。
func simpleCommands(command string) [][]string {
	tokens, err := py.ShlexSplit(command, " \t\r", outerPunctuation)
	if err != nil {
		return nil
	}
	for _, token := range tokens {
		if strings.Contains(token, "<<") && !strings.Contains(token, "<<<") && strings.Trim(token, outerPunctuation) == "" {
			return nil
		}
	}
	var commands [][]string
	var current []string
	flush := func() {
		if len(current) > 0 {
			commands = append(commands, current)
		}
		current = nil
	}
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		switch {
		case token != "" && strings.Trim(token, "<>&") == "" && strings.ContainsAny(token, "<>"):
			index++ // リダイレクトの演算子と、続くファイル名
		case token != "" && strings.Trim(token, outerPunctuation) == "":
			flush()
		default:
			current = append(current, token)
		}
	}
	flush()
	return commands
}

// stripWrappers は単純コマンドの先頭の代入・予約語・wrapper を外す（組み込みの `bL` と `att` の近似）。
// 組み込みが外す wrapper のうち、引数の形が単純な代表的なものだけを扱う。外せない形はそのまま返す（判定しない側）。
func stripWrappers(argv []string) []string {
	for len(argv) > 0 {
		head := argv[0]
		if keywords[head] || nameAssignmentRE.MatchString(head) {
			argv = argv[1:]
			continue
		}
		rest := argv[1:]
		switch baseName(head) {
		case "time", "nohup", "command", "builtin", "noglob", "exec":
			argv = skipOptions(rest, "")
		case "nice":
			argv = skipOptions(rest, "n")
		case "stdbuf":
			argv = skipOptions(rest, "ioe")
		case "sudo":
			argv = skipOptions(rest, "ugCDhprtURT")
		case "doas":
			argv = skipOptions(rest, "uC")
		case "env":
			argv = skipOptions(rest, "uSCP")
			for len(argv) > 0 && nameAssignmentRE.MatchString(argv[0]) {
				argv = argv[1:]
			}
		case "timeout":
			argv = skipOptions(rest, "sk")
			if len(argv) == 0 {
				return argv
			}
			argv = argv[1:] // 時間
		default:
			return argv
		}
	}
	return argv
}

// skipOptions は先頭のオプションを外す。withArgument に含まれる 1 文字のオプションは、続く語を値として取る。
func skipOptions(args []string, withArgument string) []string {
	for len(args) > 0 {
		arg := args[0]
		if arg == "--" {
			return args[1:]
		}
		if len(arg) < 2 || arg[0] != '-' {
			return args
		}
		args = args[1:]
		takesValue := !strings.HasPrefix(arg, "--") && len(arg) == 2 && strings.ContainsRune(withArgument, rune(arg[1]))
		if takesValue && len(args) > 0 {
			args = args[1:]
		}
	}
	return args
}

// findInlineScript は組み込みの `FTr` である。argv がシェルの -c の呼び出しなら、スクリプトと検査できる形を返す。
func findInlineScript(argv []string) (inlineScript, bool) {
	if len(argv) == 0 {
		return inlineScript{}, false
	}
	if len(argv) > 1 && (shellName(argv[0]) == "busybox" || shellName(argv[0]) == "toybox") && shells[shellName(argv[1])] {
		argv = argv[1:]
	}
	name := shellName(argv[0])
	if !shells[name] {
		return inlineScript{}, false
	}
	rest := argv[1:]
	bashLike := shellBashRE.MatchString(name)
	hasCommand, unknown := false, false
	index := 0
	for ; index < len(rest); index++ {
		option := rest[index]
		if option == "-" || option == "--" {
			index++
			break
		}
		if !shellOptionRE.MatchString(option) {
			break
		}
		var long []string
		if bashLike {
			long = shellLongRE.FindStringSubmatch(option)
		}
		switch {
		case long != nil:
			if long[1] != "" {
				index++
			}
		case strings.HasPrefix(option, "--"):
			unknown = true
		default:
			unknown = unknown || (bashLike && shellBadFlagsRE.MatchString(option[1:])) ||
				(shellKshRE.MatchString(name) && shellKshFlagRE.MatchString(option))
			hasCommand = hasCommand || (strings.HasPrefix(option, "-") && strings.Contains(option, "c"))
			index += len(shellOptionArgRE.FindAllString(option, -1))
		}
	}
	if unknown {
		for _, arg := range rest {
			if shellCommandFlag.MatchString(arg) {
				return inlineScript{script: strings.Join(rest, " ")}, true
			}
		}
		return inlineScript{}, false
	}
	if !hasCommand || index >= len(rest) {
		return inlineScript{}, false
	}
	script := rest[index]
	found := inlineScript{script: script}
	if jsLength(script) > maxInlineScript || uncheckableScript(script, name) {
		return found, true
	}
	if !positionalUseRE.MatchString(script) {
		found.judged, found.judgedOK = script, true
		return found, true
	}
	// $0 は -c の後の最初の引数（無ければシェルの名前）、$1 以降はその後の引数である。
	positional := []string{name}
	if index+1 < len(rest) {
		positional = rest[index+1:]
	}
	values := make([]string, len(positional))
	for position, arg := range positional {
		value, ok := literalValue(arg)
		if !ok {
			return found, true
		}
		values[position] = value
	}
	if hasUnknownBraceParameter(script) || strings.Contains(script, "'") || isComplexScript(script) {
		return found, true
	}
	if judged, ok := substitutePositional(script, values); ok && !isComplexScript(judged) {
		found.judged, found.judgedOK = judged, true
	}
	return found, true
}

// uncheckableScript は `FTr` のうち、スクリプトの形だけで検査を諦める条件である（括弧展開・alias・IFS・zsh 固有の展開）。
func uncheckableScript(script, name string) bool {
	for _, word := range zcnRE.FindAllString(script, -1) {
		if isBraceExpansion(word) {
			return true
		}
	}
	if aliasRE.MatchString(script) || ifsRE.MatchString(ifsReadRE.ReplaceAllLiteralString(script, " read")) {
		return true
	}
	return name == "zsh" && zshRE.MatchString(script)
}

// isBraceExpansion は組み込みの `NTr` である。語が静的に展開できない括弧展開に見えるかを返す。
// sed の `\{0,1\}` のようにクォートの中の括弧も数える（組み込みと同じ）。
func isBraceExpansion(word string) bool {
	text := braceVarRE.ReplaceAllLiteralString(word, "$X")
	opens := strings.Count(text, "{")
	if opens == 0 || !braceSplitRE.MatchString(text) {
		return false
	}
	match := braceShapeRE.FindStringSubmatch(text)
	if opens > 1 || match == nil {
		return true
	}
	head, items, tail := match[1], match[2], match[3]
	if !braceSplitRE.MatchString(items) {
		return false
	}
	if braceHeadRE.MatchString(head) || braceTailRE.MatchString(tail) {
		return true
	}
	for _, item := range braceSplitRE.Split(items, -1) {
		item = quoteCharsRE.ReplaceAllLiteralString(item, "")
		if braceItemRE.MatchString(item) || (item == "" && braceEmptyRE.MatchString(head+tail)) || expandsToRemoval(word) {
			return true
		}
	}
	return false
}

// expandsToRemoval は組み込みの `efn` である。括弧展開の候補のどれかが rm / rmdir になるかを返す。
func expandsToRemoval(word string) bool {
	match := braceShapeRE.FindStringSubmatch(quoteCharsRE.ReplaceAllLiteralString(word, ""))
	if match == nil {
		return false
	}
	for _, item := range braceSplitRE.Split(match[2], -1) {
		if removalPathRE.MatchString(match[1] + item + match[3]) {
			return true
		}
	}
	return false
}

// literalValue は位置引数の値を静的に書ける形にする（組み込みの `Se`）。
// `~` で始まる値は組み込みと同じくホームディレクトリへ展開し、展開できない形（`~user`）は検査を諦める（ok が false）。
// 静的に書けない値は inlineShellValue にする。
func literalValue(arg string) (string, bool) {
	flat := strings.NewReplacer("\t", " ", "\n", " ")
	if tildeRE.MatchString(arg) {
		if expanded := py.Home() + arg[1:]; literalArgRE.MatchString(expanded) {
			return flat.Replace(expanded), true
		}
		return inlineShellValue, true
	}
	if strings.HasPrefix(arg, "~") {
		return "", false
	}
	if literalArgRE.MatchString(arg) {
		return flat.Replace(arg), true
	}
	return inlineShellValue, true
}

// hasUnknownBraceParameter は `/\$\{(?![0-9]+(?:\}|:?\?[^}$`]*\})|[@*#]\})[!#@*0-9]/` である。
// RE2 は先読みを扱えないので、`${` の位置ごとに先読みの部分を先頭一致で確かめる。
func hasUnknownBraceParameter(script string) bool {
	for offset := 0; ; {
		found := strings.Index(script[offset:], "${")
		if found < 0 {
			return false
		}
		rest := script[offset+found+2:]
		if rest != "" && strings.ContainsRune("!#@*0123456789", rune(rest[0])) && !positionalLookRE.MatchString(rest) {
			return true
		}
		offset += found + 2
	}
}

// substitutePositional は位置引数（`$1`・`${1:?}`・`"$@"`・`$#` など）を値に置き換える（`FTr` の末尾）。
// 置き換えると意味が変わる形（クォートの外の `$@` に複数の値、奇数個のクォートの後の `"$@"`）は ok を false にする。
func substitutePositional(script string, values []string) (string, bool) {
	valueAt := func(position string) (string, bool) {
		n, err := strconv.Atoi(position)
		if err != nil || n >= len(values) {
			return "", false
		}
		return values[n], true
	}
	ok := true
	var out strings.Builder
	last := 0
	for _, match := range positionalRE.FindAllStringSubmatchIndex(script, -1) {
		whole := script[match[0]:match[1]]
		group := func(n int) (string, bool) {
			if match[2*n] < 0 {
				return "", false
			}
			return script[match[2*n]:match[2*n+1]], true
		}
		guardedNumber, isGuarded := group(1)
		colon, _ := group(2)
		name, hasName := group(3)
		if !hasName {
			name, hasName = group(4)
		}
		replacement := whole
		switch {
		case strings.HasPrefix(whole, `"`):
			before := escapeRE.ReplaceAllLiteralString(script[:match[0]], "")
			ok = ok && strings.Count(before, `"`)%2 == 0
			quoted := make([]string, 0, len(values))
			for _, value := range values[1:] {
				quoted = append(quoted, `"`+value+`"`)
			}
			replacement = strings.Join(quoted, " ")
		case isGuarded:
			value, exists := valueAt(guardedNumber)
			switch {
			case !exists:
				replacement = inlineShellGuarded
			case (value == "" || value == inlineShellValue) && colon != "":
				replacement = inlineShellGuarded
			default:
				replacement = value
			}
		case !hasName:
			// エスケープはそのまま残す。
		case name == "#":
			replacement = strconv.Itoa(len(values) - 1)
		case name == "@" || name == "*":
			ok = ok && len(values) <= 2
			replacement = ""
			if len(values) > 1 {
				replacement = values[1]
			}
		default:
			replacement, _ = valueAt(name)
		}
		out.WriteString(script[last:match[0]])
		out.WriteString(replacement)
		last = match[1]
	}
	out.WriteString(script[last:])
	return out.String(), ok
}

// isComplexScript は組み込みの `Ncn` である。関数定義・alias・IFS・位置引数への代入・`set` での置換、
// コマンドの位置にある shift・source・eval・trap・変数を含むスクリプトは、置き換えでは意味を保てない。
func isComplexScript(script string) bool {
	if ncnRE.MatchString(script) {
		return true
	}
	for _, words := range splitCommands(script, true) {
		for position, word := range words {
			if !onlyWrappersBefore(words[:position]) {
				break
			}
			if ncnBuiltinRE.MatchString(word) || startsWithUntrackedVariable(word) {
				return true
			}
			if word == "set" && setChangesPositional(words[position+1:]) {
				return true
			}
		}
	}
	return false
}

// onlyWrappersBefore は、語の前が wrapper・オプション・数だけかを返す（`Ncn` の前置きの条件）。
func onlyWrappersBefore(words []string) bool {
	for _, word := range words {
		if !commandWrappers[word] && !ncnDashRE.MatchString(word) {
			return false
		}
	}
	return true
}

// startsWithUntrackedVariable は `/^\$(?!\{?[0-9@*#])/` である。
func startsWithUntrackedVariable(word string) bool {
	if !strings.HasPrefix(word, "$") {
		return false
	}
	rest := strings.TrimPrefix(word[1:], "{")
	return rest == "" || !strings.ContainsRune("0123456789@*#", rune(rest[0]))
}

// setChangesPositional は `set` の引数が位置引数を置き換えるか（`-`・`--`・オプションでない値）を返す。
func setChangesPositional(args []string) bool {
	for position, arg := range args {
		previous := ""
		if position > 0 {
			previous = args[position-1]
		}
		if arg == "-" || arg == "--" || (!ncnFlagRE.MatchString(arg) && !ncnOptionArgRE.MatchString(previous)) {
			return true
		}
	}
	return false
}

// splitCommands は組み込みの `u$` である。スクリプトを区切りで分け、各断片の語（リダイレクトと先頭の代入を除く）を返す。
// quoted が true ならクォートの中の区切りと空白を区切りとして扱わない。false なら区切りとして扱う（組み込みは両方で調べる）。
func splitCommands(script string, quoted bool) [][]string {
	text := script
	if quoted {
		text = usQuotedRE.ReplaceAllStringFunc(text, func(part string) string {
			if strings.HasPrefix(part, `\`) {
				return part
			}
			return usQuotedCharsRE.ReplaceAllLiteralString(part, "\x00")
		})
	}
	text = replaceDuplications(text)
	text = usRedirectRE.ReplaceAllLiteralString(text, ">")
	text = stripQuotes(text)
	var commands [][]string
	for _, part := range usSplitRE.Split(text, -1) {
		var raw []string
		for _, word := range usWordSplitRE.Split(part, -1) {
			if word != "" {
				raw = append(raw, word)
			}
		}
		var kept []string
		leadingAssignments := true
		for position, word := range raw {
			isAssignment := usAssignmentRE.MatchString(word)
			afterRedirect := position > 0 && usRedirectOnlyRE.MatchString(raw[position-1])
			if !usRedirectWordRE.MatchString(word) && !afterRedirect && (!isAssignment || !leadingAssignments) {
				kept = append(kept, word)
			}
			leadingAssignments = leadingAssignments && isAssignment
		}
		commands = append(commands, kept)
	}
	return commands
}

// stripQuotes は `/\$(?=['"])|['"\\]/g` を空にする。先読みの `$` は、続くクォートと一緒に消す形に展開した
// （クォートはどのみち同じ置き換えで消える）。
func stripQuotes(text string) string {
	return usStripQuotesRE.ReplaceAllLiteralString(text, "")
}

// replaceDuplications は `/\d*[<>]&(?:\d+-?|-)(?![^\s;&|()])/g` を空白に置き換える。
// 先読みは一致の直後の 1 文字を確かめる形にした。短い一致へ戻っても直後は数字か `-` になり先読みを満たさないので、
// 最長の一致だけ見ればよい。
func replaceDuplications(text string) string {
	var out strings.Builder
	last := 0
	for _, match := range usDupRE.FindAllStringIndex(text, -1) {
		if match[1] < len(text) {
			next, _ := utf8.DecodeRuneInString(text[match[1]:])
			if !isJSSpace(next) && !strings.ContainsRune(";&|()", next) {
				continue
			}
		}
		out.WriteString(text[last:match[0]])
		out.WriteString(" ")
		last = match[1]
	}
	out.WriteString(text[last:])
	return out.String()
}

// needsInlineCheck は組み込みの `Jcn` である。スクリプトの中身を検査する必要があるか
// （括弧展開・rm の語・区切りの直後の変数のどれかを含むか）を返す。
func needsInlineCheck(script string) bool {
	text := stripQuotes(script)
	return jcnBraceRE.MatchString(text) || jcnRemovalRE.MatchString(text) || jcnVariableRE.MatchString(text)
}

// mayRunRemoval は組み込みの `Dwe` である。スクリプトが rm / rmdir を実行しうるか
// （wrapper やオプションと並んだ rm、コマンドの位置の rm や変数、rm を指す代入や括弧展開）を返す。
// コマンドの位置の判定は、直前の語が wrapper かで見る。`direnv exec "$DIR"` の `exec` も wrapper とみなす（組み込みと同じ）。
func mayRunRemoval(script string) bool {
	isRemoval := func(word string) bool {
		return removalWordRE.MatchString(baseName(redirectTailRE.ReplaceAllLiteralString(word, ""))) ||
			removalListRE.MatchString(word)
	}
	isWrapper := func(word string) bool { return commandWrappers[strings.ToLower(baseName(word))] }
	for _, words := range append(splitCommands(script, true), splitCommands(script, false)...) {
		for position, word := range words {
			if isRemoval(word) {
				for other, neighbor := range words {
					if other != position && (isWrapper(neighbor) ||
						(shortFlagsRE.MatchString(neighbor) && strings.Contains(neighbor, "c")) || findExecRE.MatchString(neighbor)) {
						return true
					}
				}
			}
			if (isRemoval(word) || strings.HasPrefix(word, "$")) && (position == 0 || isWrapper(words[position-1])) {
				return true
			}
			if removalAssignRE.MatchString(word) {
				return true
			}
		}
	}
	for _, word := range zcnRE.FindAllString(script, -1) {
		if expandsToRemoval(word) {
			return true
		}
	}
	return false
}

// countDirectoryChanges はスクリプトの語のうち cd・pushd・popd・chdir の数である。
func countDirectoryChanges(script string) int {
	count := 0
	for _, words := range splitCommands(script, true) {
		for _, word := range words {
			if directoryChangeRE.MatchString(baseName(word)) {
				count++
			}
		}
	}
	return count
}

// inlineCheckDisabled は組み込みの真偽値の環境変数の読み方（`Ne`: 1・true・yes・on を真とする）で disableInlineEnv を読む。
func inlineCheckDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(disableInlineEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// findInlineShell は shell -c の 2 分岐を判定し、拒否するなら発火した分岐を返す。
// 外側の各単純コマンドについて、スクリプトを検査できなければ `Lwe`、検査できれば中身を再帰的に判定する。
func findInlineShell(command, cwd string, depth int) (finding, error) {
	if inlineCheckDisabled() {
		return finding{}, nil
	}
	// 外側の cd の有無は、shell -c が何個あっても 1 回だけ調べる（入力の長さの 2 乗にしない）。
	outerCD, checkedCD := false, false
	for _, argv := range simpleCommands(command) {
		found, ok := findInlineScript(stripWrappers(argv))
		if !ok {
			continue
		}
		judged := found.script
		if found.judgedOK {
			judged = found.judged
		}
		if !needsInlineCheck(judged) && !strings.Contains(found.script, "$'") {
			continue
		}
		unchecked := !found.judgedOK || depth >= maxInlineDepth || jsLength(judged) > maxInlineScript ||
			countDirectoryChanges(judged) > maxInlineDirectoryChanges
		if unchecked {
			if mayRunRemoval(judged) {
				return finding{target: idInlineUnchecked, why: idWhyInlineUnchecked, how: idHowInlineUnchecked}, nil
			}
			continue
		}
		if !checkedCD {
			outerCD, checkedCD = hasDirectoryChange(command), true
		}
		if outerCD {
			continue
		}
		inner := strings.ReplaceAll(judged, inlineShellValue, "${"+inlineShellValue+"}")
		result, err := evaluateAt(inner, cwd, depth+1)
		if err != nil {
			return finding{}, err
		}
		if result.target == "" {
			continue
		}
		if strings.Contains(result.token, "__INLINE_SHELL_") {
			// 置き換え先の名前は書いた覚えのない文字列なので、組み込みの `LKe` と同じく `${…}` に戻して見せる。
			token := strings.NewReplacer(inlineShellGuarded, "${…:?}", "${"+inlineShellValue+"}", "${…}").Replace(result.token)
			result = finding{target: idInlineValue, command: result.command, token: token,
				why: idWhyInlineValue, how: idHowInlineValue}
		}
		return result, nil
	}
	return finding{}, nil
}

// --- Claude Code v2.1.309 の「シェルが別の読み方をし得る」の検査からの移植 ---------------------
//
// v2.1.309 は、組み込みのパーサー（tree-sitter ではなく JS で書いた bash の再実装）の結果の根が ERROR になると、
// rm が無くても dangerousRemoval の ask にする（`bKe` の too-complex の分岐。"This command could not be checked
// for dangerous rm operations: the shell may read it differently than it was parsed."）。
// 最上位のコマンドだけが対象で、shell -c の中の再帰検査では出ない。
//
// 根が ERROR になるのは、解析の途中で zshBraceDiff・zshCondParenDiff・keywordDesync のどれかが立ったときである。
// 立てる箇所は 30 近くあるが、移したのは次の 2 箇所だけである。
//   - `${` の直後の文法（`Pe`）が読み切れず、続く文字が `}` でない（`${(f)"$(…)"}`・`${(M)FILES:#*.rb}` のような
//     zsh のパラメータフラグ）。過去の Bash のコマンド 47,774 件を組み込みのパーサーに通すと、根が ERROR になった 16 件は
//     すべてこの箇所だった。
//   - `$(` の中を読み終えた位置が `)` でない。組み込みのトークナイザは `))` を 1 つの字句として読むので、
//     文の位置のサブシェル（区切りや `!` の直後の `(`）と配列の代入（`x=(…)`）が `))` で閉じると、2 文字とも
//     その閉じ括弧として読まれ、外側の `$(` の閉じ括弧が無くなる（`$(a || (b))`・`"$( (a))"`・`$(x=(a b))`）。
//     間の `(` や `<(` も同じく 1 つずつずれるので、`$(` の中のどの深さでも起きる。`$(` の外（`(a && (b))`）、
//     算術式（`$((…))`・`((…))`）の中、引数の位置の `(`（`echo (a))`。読み戻すのでずれない）では起きない。
//     過去の Bash のコマンド 161,423 件では、上の `${` の分岐以外で根が ERROR になった実物はすべてこの形だった。
//
// 移植していない条件（組み込みでは ask になるが、ここでは通す）:
//   - `${` の後ろの演算子（`:-`・`/`・`#` など）より後ろの読み方。`${x:-{a}}` の `{` などでも根が ERROR になる。
//   - 配列の要素・パターン・クォートの走査の中の `$(`・`` ` ``・`{`、閉じない `$(`、`$\` と改行、
//     `[[(`（zshCondParenDiff）、代入やリダイレクトの直後の予約語（keywordDesync。`FOO=1 if …`）。
//   - `))` のうち、bash では構文エラーになる位置の `(`（先行代入・リダイレクト・`do` の後ろ、`f() (…))` の本体）と、
//     宣言のコマンドの引数の配列（`local x=(a))`）。`time`・`{`・`if` の後ろの `(` は組み込みでも通る。
//
// 組み込みとずれ得る点（逆向き）: bash でも構文エラーになる入力（`$( (a)))` の余分な `)`、配列の要素の `(`）では、
// 組み込みが通すものを止めることがある。ランダムに組み立てた 30 万件で、bash の構文として正しい入力での食い違いは無かった。
//
// 組み込みとずれ得る点: 組み込みは解析が時間の上限（50ms）を超えると打ち切り、この分岐に来ない。ここでは長さによらず止める。

// 走査の文脈。
const (
	scanCode     = iota // クォートの外（最上位・`$(…)`・バッククォート）
	scanSubshell        // クォートの外の `(…)`（閉じ括弧で `$(` を閉じないように数える）
	scanBrace           // クォートの外の `${…}` の中
	scanDquote          // ダブルクォートの中
	scanBraceDQ         // ダブルクォートの中の `${…}` の中（シングルクォートは文字として読む）
	scanHeredoc         // クォートしない heredoc の本文（クォートは文字で、`$`・`\`・バッククォートだけが働く）
)

// scanFrame は走査の文脈の 1 段である。backtick ならバッククォートで閉じるコマンド置換である。
type scanFrame struct {
	mode     int
	backtick bool
	cmdsub   bool // `$(` のコマンド置換（算術式の中の `$(` は組み込みが別に読むので含めない）
	arith    bool // 算術式（`$((…))`・`((…))`）の中。中の `(` は文にならない
	array    bool // 配列の代入の `(`。中の `(` は要素であって文にならない
	closes   bool // 閉じ括弧が直後の `)` と合わせて `))` の字句として読まれる `(`（文の位置のサブシェルと配列の代入）
	inCmdsub bool // `(` の段だけを外へ辿って `$(` に届くか（`))` で閉じ括弧が 1 つずれると、その `$(` が閉じなくなる）
	start    int  // 開き括弧の位置（理由文の表記に使う）
}

// findUnparsed は、組み込みのパーサーが読み切れない形（`${…}` か `$(…)` の中の `))`）を探し、最初に見つけた分岐を返す。
// 展開として読む文脈（クォートの外・ダブルクォートの中・コマンド置換・クォートしない heredoc の本文）だけを見る。
// シングルクォート・`$'…'`・コメント・クォートした heredoc の本文の中は見ない。クォートが閉じない入力は判定しない。
func findUnparsed(command string) finding {
	parens := strings.Contains(command, "$(") && strings.Contains(command, "))")
	if !parens && !strings.Contains(command, "${") {
		return finding{}
	}
	return scanExpansions(command, scanCode)
}

// expansionScanner は scanExpansions の走査の状態である。
type expansionScanner struct {
	command   string
	found     finding // 最初に見つけた読み切れない形
	stack     []scanFrame
	pending   []heredocMarker // 行末で本文を読む heredoc
	wordStart bool            // 直前が語の区切りか（`#` をコメントとして読むか）
	cmdStart  bool            // 次の語が文の先頭か（`(` をサブシェルとして読むか）
}

// scanExpansions は base の文脈から command を走査し、最初に読み切れない形を返す。
// 見つけても末尾まで走査し、閉じない heredoc やクォートがあれば判定しない（空を返す）。
func scanExpansions(command string, base int) finding {
	s := &expansionScanner{command: command, stack: []scanFrame{{mode: base}}, wordStart: true, cmdStart: true}
	for i := 0; i < len(command); i++ {
		next, ok := s.step(i)
		if !ok {
			return finding{}
		}
		i = next
	}
	if len(s.pending) > 0 {
		return finding{} // 本文の無い heredoc も閉じていない
	}
	return s.found
}

func (s *expansionScanner) push(mode int) { s.pushFrame(scanFrame{mode: mode}) }

// pushFrame は frame を積む。算術式の中であることは内側へ引き継ぎ、`(` の段は外側の `$(` に届くかを決める。
func (s *expansionScanner) pushFrame(frame scanFrame) {
	parent := s.stack[len(s.stack)-1]
	frame.arith = frame.arith || parent.arith
	if frame.mode == scanSubshell {
		frame.inCmdsub = parent.cmdsub || parent.mode == scanSubshell && parent.inCmdsub
	}
	s.stack = append(s.stack, frame)
}

func (s *expansionScanner) pop() {
	if len(s.stack) > 1 {
		s.stack = s.stack[:len(s.stack)-1]
	}
}

// step は位置 i の 1 文字（または 1 つの字句）を読み、最後に読んだ位置を返す。判定をやめるなら ok は false である。
func (s *expansionScanner) step(i int) (last int, ok bool) {
	command := s.command
	top := s.stack[len(s.stack)-1]
	c := command[i]
	switch {
	case c == '\\':
		s.wordStart, s.cmdStart = false, false
		return i + 1, true
	case c == '$' && i+1 < len(command) && command[i+1] == '{':
		if s.found.target == "" && !expansionHeadParses(command, i+2) {
			s.found = finding{target: idUnparsed, token: expansionShown(command, i), why: idWhyUnparsed, how: idHowUnparsed}
		}
		if top.mode >= scanDquote {
			s.push(scanBraceDQ)
		} else {
			s.push(scanBrace)
		}
		s.wordStart, s.cmdStart = false, false
		return i + 1, true
	case c == '$' && i+2 < len(command) && command[i+1] == '(' && command[i+2] == '(':
		// 算術式の展開。組み込みは `$((` を算術式として読み、中の `(` と `$(` を文として読まない。
		s.pushFrame(scanFrame{mode: scanCode, arith: true, start: i})
		s.pushFrame(scanFrame{mode: scanSubshell, arith: true, start: i + 1})
		s.wordStart, s.cmdStart = true, false
		return i + 2, true
	case c == '$' && i+1 < len(command) && command[i+1] == '(':
		s.pushFrame(scanFrame{mode: scanCode, cmdsub: !top.arith, start: i})
		s.wordStart, s.cmdStart = true, true
		return i + 1, true
	case c == '$' && i+1 < len(command) && command[i+1] == '\'' && top.mode < scanDquote:
		s.wordStart, s.cmdStart = false, false
		return skipANSIC(command, i+2), true
	case c == '`':
		if top.backtick {
			s.pop()
		} else {
			s.stack = append(s.stack, scanFrame{mode: scanCode, backtick: true})
		}
		s.wordStart, s.cmdStart = true, !top.backtick
		return i, true
	}
	if top.mode >= scanDquote {
		s.cmdStart = false
	}
	switch top.mode {
	case scanHeredoc:
	case scanDquote:
		if c == '"' {
			s.pop()
		}
	case scanBraceDQ:
		switch c {
		case '"':
			s.push(scanDquote)
		case '}':
			s.pop()
		}
	default:
		last, ok = s.stepUnquoted(i, top)
		s.wordStart = strings.IndexByte(" \t\n;&|()<>", c) >= 0
		return last, ok
	}
	return i, true
}

// stepUnquoted はクォートの外（scanCode・scanSubshell・scanBrace）の 1 文字を読む。
func (s *expansionScanner) stepUnquoted(i int, top scanFrame) (last int, ok bool) {
	command := s.command
	c := command[i]
	inBrace := top.mode == scanBrace
	cmdStart := s.cmdStart
	// 文の先頭を保つのは、空白・区切り・単独の `!`・コメントだけである。それ以外の文字は語を始める。
	switch {
	case c == ' ' || c == '\t' || c == '#' && s.wordStart:
	case c == '\n' && top.array:
		// 組み込みは配列の要素を改行の手前までしか読まず、残りを文として読み直す。閉じ括弧の位置がずれなくなる。
		s.stack[len(s.stack)-1].closes = false
		s.cmdStart = true
	case c == ';' || c == '&' || c == '|' || c == '\n':
		s.cmdStart = true
	case c == '!' && i+1 < len(command) && (command[i+1] == ' ' || command[i+1] == '\t'):
	default:
		s.cmdStart = false
	}
	if cmdStart && !inBrace && !top.arith && !top.array {
		if open := arrayAssignmentParen(command, i); open >= 0 {
			s.pushFrame(scanFrame{mode: scanSubshell, array: true, closes: true, start: open})
			return open, true
		}
	}
	switch {
	case c == '\'':
		end := strings.IndexByte(command[i+1:], '\'')
		if end < 0 {
			return i, false
		}
		return i + end + 1, true
	case c == '"':
		s.push(scanDquote)
	case c == '}' && inBrace:
		s.pop()
	case inBrace:
	case c == '(' && cmdStart && !top.arith && !top.array && i+1 < len(command) && command[i+1] == '(':
		// 文の位置の `((` は算術式のコマンドで、`))` で閉じる。
		s.pushFrame(scanFrame{mode: scanSubshell, arith: true, start: i})
		s.pushFrame(scanFrame{mode: scanSubshell, arith: true, start: i + 1})
		return i + 1, true
	case c == '(':
		closes := cmdStart && !top.arith && !top.array
		s.pushFrame(scanFrame{mode: scanSubshell, closes: closes, start: i})
		s.cmdStart = !top.arith && !top.array
	case c == ')' && !top.backtick:
		s.pop()
		if top.closes && top.inCmdsub && i+1 < len(command) && command[i+1] == ')' && s.found.target == "" {
			s.found = finding{target: idUnparsedParens, token: parensShown(command, top.start, i+2),
				why: idWhyUnparsedParens, how: idHowUnparsedParens}
		}
		// 配列の代入は先行代入なので、閉じた後も文の先頭が続く。
		s.cmdStart = top.array
	case c == '#' && s.wordStart:
		if end := strings.IndexByte(command[i:], '\n'); end >= 0 {
			return i + end - 1, true
		}
		return len(command) - 1, true
	case strings.HasPrefix(command[i:], "<<<"):
		return i + 2, true // here-string は heredoc ではない
	case strings.HasPrefix(command[i:], "<<"):
		marker, next := readHeredocMarker(command, i+2)
		if marker.delimiter != "" {
			s.pending = append(s.pending, marker)
		}
		return next - 1, true
	case c == '\n' && len(s.pending) > 0:
		return s.readHeredocBodies(i)
	}
	return i, true
}

// arrayAssignmentParen は i から始まる語が配列の代入（`name=(`・`name+=(`・`name[…]=(`）なら、その `(` の位置を返す（違えば -1）。
func arrayAssignmentParen(command string, i int) int {
	isNameStart := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' }
	if i >= len(command) || !isNameStart(command[i]) {
		return -1
	}
	j := i + 1
	for j < len(command) && (isNameStart(command[j]) || command[j] >= '0' && command[j] <= '9') {
		j++
	}
	if j < len(command) && command[j] == '[' {
		end := strings.IndexAny(command[j:min(len(command), j+maxSubscript)], "]\n")
		if end < 0 || command[j+end] != ']' {
			return -1
		}
		j += end + 1
	}
	if j < len(command) && command[j] == '+' {
		j++
	}
	if j+1 < len(command) && command[j] == '=' && command[j+1] == '(' {
		return j + 1
	}
	return -1
}

// parensShown は理由文に添える `(…))` の表記である。長いときは `))` に近い末尾だけを見せる。
func parensShown(command string, start, end int) string {
	const limit = 40
	if end-start <= limit {
		return command[start:end]
	}
	cut := end - limit
	for cut < end && !utf8.RuneStart(command[cut]) {
		cut++
	}
	return "…" + command[cut:end]
}

// readHeredocBodies は改行（i）の後ろに続く heredoc の本文を順に読み、最後の区切りの行の末尾の手前を返す。
func (s *expansionScanner) readHeredocBodies(i int) (last int, ok bool) {
	next := i + 1
	for _, marker := range s.pending {
		var body string
		var closed bool
		body, next, closed = heredocBody(s.command, next, marker)
		if !closed {
			// 区切りの行で閉じない heredoc は、組み込みのパーサーが解析を打ち切ることがある（その場合は ask にならない）。
			return i, false
		}
		if !marker.quoted && s.found.target == "" {
			s.found = scanExpansions(body, scanHeredoc)
		}
	}
	s.pending = nil
	s.cmdStart = true
	return next - 1, true
}

// expansionHeadParses は組み込みの `Pe` のうち、`${` の直後（i）から名前と添字までを読み、続く文字で `}` に届くかを返す。
// 演算子（`:`・`#`・`%`・`/`・`^`・`,`・`-`・`=`・`?`・`+`）が続けば、その後ろは読み切れるものとみなす（通す側）。
// 添字の `]` が見つからないときも通す側に倒す。
func expansionHeadParses(command string, i int) bool {
	at := func(offset int) byte {
		if i+offset < len(command) {
			return command[i+offset]
		}
		return 0
	}
	isNameStart := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' }
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	isNameChar := func(c byte) bool { return isNameStart(c) || isDigit(c) }
	// 組み込みの `x`: 空白・タブ・CR と、バックスラッシュと改行を飛ばす。
	skipBlanks := func() {
		for {
			switch {
			case at(0) == ' ' || at(0) == '\t' || at(0) == '\r':
				i++
			case at(0) == '\\' && at(1) == '\n':
				i += 2
			default:
				return
			}
		}
	}
	skipBlanks()
	// `${#!}` と `${!#}`・`${!##}`（`}` の前に空白を 1 つまで挟める）は特別な形として読む。
	if at(0) == '#' && at(1) == '!' && at(2) == '}' {
		return true
	}
	if at(0) == '!' && at(1) == '#' {
		offset := 2
		if at(offset) == '#' {
			offset++
		}
		if at(offset) == ' ' {
			offset++
		}
		if at(offset) == '}' {
			return true
		}
	}
	if at(0) == '#' {
		i++
	}
	if c := at(0); (c == '!' || c == '=' || c == '~') && (isNameStart(at(1)) || isDigit(at(1))) {
		i++
	}
	skipBlanks()
	switch c := at(0); {
	case isNameStart(c):
		for isNameChar(at(0)) {
			i++
		}
	case isDigit(c):
		for isDigit(at(0)) {
			i++
		}
	case c != 0 && strings.IndexByte("?$@*#-!_", c) >= 0:
		i++
	}
	if at(0) == '[' {
		// zsh の添字フラグ（`${x[(r)pat]}`）。組み込みは `(…)` の後ろに `]` 以外が続く添字を読み切れない。
		open := i + 1
		for open < len(command) && (command[open] == ' ' || command[open] == '\t') {
			open++
		}
		if open+1 < len(command) && command[open] == '(' && command[open+1] != '(' {
			window := command[open:min(len(command), open+maxSubscript)]
			if end := strings.IndexByte(window, ')'); end >= 0 && open+end+1 < len(command) &&
				command[open+end+1] != ']' {
				return false
			}
		}
		end := subscriptEnd(command, i+1)
		if end < 0 {
			return true
		}
		i = end + 1
	}
	skipBlanks()
	if c := at(0); (c == '*' || c == '@') && at(1) == '}' {
		return true
	}
	if at(0) == '@' && isNameStart(at(1)) {
		i++
		for isNameChar(at(0)) {
			i++
		}
	} else if c := at(0); c != 0 && strings.IndexByte(":#%/^,-=?+", c) >= 0 {
		return true
	}
	for at(0) == '\n' {
		i++
	}
	return at(0) == '}'
}

// maxSubscript は添字を読む長さの上限である。`${x[` を繰り返す入力で走査が入力の長さの 2 乗にならないようにする。
// 上限までに `]` が無ければ通す側に倒す。
const maxSubscript = 256

// subscriptEnd は添字の `[` の直後（i）から、対応する `]` の位置を返す（無ければ -1）。クォートとエスケープを飛ばす。
func subscriptEnd(command string, i int) int {
	depth := 0
	for limit := min(len(command), i+maxSubscript); i < limit; i++ {
		switch command[i] {
		case '\\':
			i++
		case '\'', '"':
			end := strings.IndexByte(command[i+1:limit], command[i])
			if end < 0 {
				return -1
			}
			i += end + 1
		case '[':
			depth++
		case ']':
			if depth == 0 {
				return i
			}
			depth--
		case '}', '\n':
			return -1
		}
	}
	return -1
}

// expansionShown は理由文に添える `${…}` の表記（対応する `}` まで）である。長い展開は先頭だけを見せる。
func expansionShown(command string, start int) string {
	const limit = 40
	end, depth := len(command), 0
scan:
	for i := start + 1; i < len(command); i++ {
		switch command[i] {
		case '\\':
			i++
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				end = i + 1
				break scan
			}
		}
	}
	if end-start <= limit {
		return command[start:end]
	}
	cut := start + limit
	for cut > start && !utf8.RuneStart(command[cut]) {
		cut--
	}
	return command[start:cut] + "…"
}

// skipANSIC は `$'` の直後（i）から、閉じる `'` の位置を返す（閉じなければ末尾）。
func skipANSIC(command string, i int) int {
	for ; i < len(command); i++ {
		switch command[i] {
		case '\\':
			i++
		case '\'':
			return i
		}
	}
	return len(command)
}

// heredocMarker は `<<` の区切りの語である。quoted なら本文を展開しない。stripTabs は `<<-` である。
type heredocMarker struct {
	delimiter string
	quoted    bool
	stripTabs bool
}

// readHeredocMarker は `<<` の直後（i）から区切りの語を読み、語の直後の位置を返す。
func readHeredocMarker(command string, i int) (heredocMarker, int) {
	var marker heredocMarker
	if i < len(command) && command[i] == '-' {
		marker.stripTabs = true
		i++
	}
	for i < len(command) && (command[i] == ' ' || command[i] == '\t') {
		i++
	}
	var word strings.Builder
	for i < len(command) {
		c := command[i]
		if c == '\'' || c == '"' {
			end := strings.IndexByte(command[i+1:], c)
			if end < 0 {
				return heredocMarker{}, len(command)
			}
			word.WriteString(command[i+1 : i+1+end])
			marker.quoted = true
			i += end + 2
			continue
		}
		if c == '\\' && i+1 < len(command) {
			word.WriteByte(command[i+1])
			marker.quoted = true
			i += 2
			continue
		}
		if strings.IndexByte(" \t\n;&|()<>", c) >= 0 {
			break
		}
		word.WriteByte(c)
		i++
	}
	marker.delimiter = word.String()
	return marker, i
}

// heredocBody は本文の先頭（i）から区切りの行の手前までを本文として返し、区切りの行の末尾（改行の位置）を返す。
// 区切りの行が無ければ closed を false にする。
func heredocBody(command string, i int, marker heredocMarker) (body string, next int, closed bool) {
	start := i
	for i <= len(command) {
		lineEnd := len(command)
		if end := strings.IndexByte(command[i:], '\n'); end >= 0 {
			lineEnd = i + end
		}
		line := command[i:lineEnd]
		if marker.stripTabs {
			line = strings.TrimLeft(line, "\t")
		}
		if line == marker.delimiter {
			return command[start:i], lineEnd, true
		}
		// `EOF; echo` のように区切りの語の後ろに区切り文字が続く行は、組み込みのパーサーの読み方が定まらない
		// （解析を打ち切ることがある）ので、閉じていない扱いにして判定しない。
		if rest, ok := strings.CutPrefix(line, marker.delimiter); ok && rest != "" &&
			strings.IndexByte(" \t;&|()<>", rest[0]) >= 0 {
			return "", 0, false
		}
		if lineEnd == len(command) {
			break
		}
		i = lineEnd + 1
	}
	return command[start:], len(command), false
}
