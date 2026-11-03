package dangerousrmguard

import (
	"strings"
	"unicode/utf8"
)

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

// spellsRemoval は command[i] の r と m の間（m なら前の r との間）に、バックスラッシュとクォートだけが挟まるかを返す
// （rm・rmdir・`r\m`・`r”m`・`'r'm`）。走査がクォートの中を読み飛ばしても、どちらかの文字は読む位置に残る。
func spellsRemoval(command string, i int) bool {
	const glue = "\\\"'"
	switch command[i] {
	case 'r':
		j := i + 1
		for j < len(command) && strings.IndexByte(glue, command[j]) >= 0 {
			j++
		}
		return j < len(command) && command[j] == 'm'
	case 'm':
		j := i - 1
		for j >= 0 && strings.IndexByte(glue, command[j]) >= 0 {
			j--
		}
		return j < i-1 && j >= 0 && command[j] == 'r'
	}
	return false
}

// shellReading は、クォート・heredoc・コメントを解釈して command を読み、展開として読む位置のコマンド置換の数と、
// 読み飛ばさない位置にある rm の綴り（spellsRemoval）の数を返す。シングルクォート・`$'…'`・コメント・クォートした
// heredoc の本文の中は数えない。クォートや heredoc が閉じず読み切れなければ ok は false である。
func shellReading(command string) (cmdsubs, rmSpellings int, ok bool) {
	s, ok := runScanner(command, scanCode)
	if !ok {
		return 0, 0, false
	}
	return s.cmdsubs, s.rmSpellings, true
}

// expansionScanner は scanExpansions の走査の状態である。
type expansionScanner struct {
	command     string
	found       finding // 最初に見つけた読み切れない形
	stack       []scanFrame
	pending     []heredocMarker // 行末で本文を読む heredoc
	wordStart   bool            // 直前が語の区切りか（`#` をコメントとして読むか）
	cmdStart    bool            // 次の語が文の先頭か（`(` をサブシェルとして読むか）
	cmdsubs     int             // 展開として読む位置のコマンド置換（`$(`・バッククォート）の数
	rmSpellings int             // 読み飛ばさない位置にある rm の綴りの数（spellsRemoval）
}

// scanExpansions は base の文脈から command を走査し、最初に読み切れない形を返す。
// 見つけても末尾まで走査し、閉じない heredoc やクォートがあれば判定しない（空を返す）。
func scanExpansions(command string, base int) finding {
	s, ok := runScanner(command, base)
	if !ok {
		return finding{}
	}
	return s.found
}

// runScanner は base の文脈から command を末尾まで走査する。閉じない heredoc やクォートがあれば ok は false である。
func runScanner(command string, base int) (s *expansionScanner, ok bool) {
	s = &expansionScanner{command: command, stack: []scanFrame{{mode: base}}, wordStart: true, cmdStart: true}
	for i := 0; i < len(command); i++ {
		next, stepped := s.step(i)
		if !stepped {
			return s, false
		}
		i = next
	}
	return s, len(s.pending) == 0 // 本文の無い heredoc も閉じていない
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
	if spellsRemoval(command, i) {
		s.rmSpellings++
	}
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
		if !top.arith {
			s.cmdsubs++
		}
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
			s.cmdsubs++
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
//
//nolint:cyclop // bash の字句を 1 文字ずつ読む状態機械で、分岐は文字の種類ごとの規則に対応する。分けると状態の受け渡しが増え、判定が変わり得る。
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
			s.found = finding{
				target: idUnparsedParens, token: parensShown(command, top.start, i+2),
				why: idWhyUnparsedParens, how: idHowUnparsedParens,
			}
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
		if !marker.quoted {
			// クォートしない本文の置換と rm の綴りは展開されるので数える。
			sub, subOK := runScanner(body, scanHeredoc)
			if s.found.target == "" && subOK {
				s.found = sub.found
			}
			s.cmdsubs += sub.cmdsubs
			s.rmSpellings += sub.rmSpellings
		}
	}
	s.pending = nil
	s.cmdStart = true
	return next - 1, true
}
