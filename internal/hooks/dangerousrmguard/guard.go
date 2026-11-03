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
//     v2.1.283 の判定を（detect.go）、shell -c のスクリプトの検査は v2.1.296 の判定を（inlineshell.go・inlinescript.go）、
//     読み切れない `${…}` と `$(…)` の検査は v2.1.309 の判定を（unparsed.go・expansion.go）移した。
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
	// 移植元との違い: 組み込みは重要ディレクトリ・作業ディレクトリの判定をパーサーの読んだコマンドで行い、
	// クォートの中の rm（echo 'cd junk && rm -rf .'）では ask を出さない（v2.1.317 で確認、docs/verify-claude-code-asks.md）。
	// クォートを見ずに区切る移植の判定はそれを止めていたので、シングルクォート・`$'…'`・クォートした heredoc の本文の
	// 外に rm の綴りが 1 つも無いときは見ない。外の綴りは語の位置を問わず数え、`"rm"`・`r\m`・`r''m` も rm として数える
	// （読み違えても従来どおり見る側に倒れる）。
	// heredoc を含むときは従来どおり見る。シェルへ流し込む本文（bash <<'EOF'）の rm は実行されるので、
	// 組み込みが ask を出さなくても止める側に残す（同じく v2.1.317 で確認）。
	reading := struct {
		cmdsubs, rmSpellings int
		ok                   bool
	}{}
	reading.cmdsubs, reading.rmSpellings, reading.ok = shellReading(command)
	kind, token, resolved := "", "", ""
	var err error
	if !reading.ok || reading.rmSpellings > 0 || strings.Contains(command, "<<") {
		kind, token, resolved, err = findProtectedPath(command, cwd)
		if err != nil {
			return finding{}, err
		}
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
	// 移植元との違い: 組み込みはクォートした heredoc の本文の `$(…)` やバッククォートを数えない（v2.1.317 で確認）。
	// 本文に Markdown のコードスパンを書いたプランの書き出しまで止めていたので、読み切れるときは展開として読む位置だけを数える。
	count := len(substitutions(command))
	if reading.ok {
		count = reading.cmdsubs
	}
	if count > maxSubstitutions {
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
