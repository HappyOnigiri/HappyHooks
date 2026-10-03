package generatededitguard

import (
	"strings"
)

// この hook の字句解析は、書き込み先を拾うためだけのものである。他の hook のトークナイザとは共有しない（誤爆の条件が違う）。
// シェルの文法を完全には再現しない。拾い漏れは README の Limits に書く方針で、迷ったら書き込み先の候補を多めに拾う
// （候補は実在する生成ファイルでなければ判定に影響しないため）。

// word はシェルの語 1 つである。text はクォートとエスケープを外した値である。
type word struct {
	text string
	// dynamic は変数展開・コマンド置換を含み、値が静的に決まらないことを示す。
	dynamic bool
	// glob はクォートの外に glob の文字（* ? [）があることを示す。
	glob bool
	// tilde はクォートの外の ~ で始まることを示す（シェルがホームへ展開する）。
	tilde bool
	// brace はクォートの外に { があることを示す（ブレース展開の候補）。
	brace bool
}

// redirect はリダイレクト 1 つである。
type redirect struct {
	// op は演算子（> >> >| &> &>> >& < << <<- <<< <> <&）で、fd の数字は含めない。
	op     string
	target word
	// body はヒアドキュメントの本文である（op が << と <<- のときだけ）。
	body string
}

// command は単純コマンド 1 つである。
type command struct {
	words     []word
	redirects []*redirect
	// pipedFrom はパイプで stdin をつないだ直前のコマンドである。
	pipedFrom *command
}

// stepKind は step の種類である。
type stepKind int

const (
	stepCommand stepKind = iota
	// stepEnter と stepLeave はサブシェル（( ... ) と $( ... )）の出入りで、中の cd を閉じたときに取り消すために使う。
	stepEnter
	stepLeave
)

// step は、作業ディレクトリを辿る順に並べたコマンドとサブシェルの出入りである。
type step struct {
	kind    stepKind
	command *command
}

// maxNesting はコマンド置換と `sh -c` の入れ子を追う深さの上限である。悪意のある入力で再帰が深くならないようにする。
const maxNesting = 8

// parseShell は command をコマンドの並びに分ける。解釈できない部分は読み飛ばし、エラーにはしない。
func parseShell(source string, depth int) []step {
	p := &parser{src: source, depth: depth}
	p.run()
	return p.steps
}

type parser struct {
	src   string
	pos   int
	depth int
	steps []step
	cur   *command
	// piped は、次のコマンドの stdin が直前のコマンドからパイプでつながることを示す。
	piped *command
	// pending は本文をまだ読んでいないヒアドキュメントで、改行のたびに出現順に本文を読む。
	pending []*redirect
	// pendingStrip は pending の各ヒアドキュメントが <<- （行頭のタブを除く）かを示す。
	pendingStrip []bool
	// subshells は開いているサブシェルの数である。対応しない ) で出る step を作らないために数える。
	subshells int
}

func (p *parser) run() {
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == ' ' || c == '\t':
			p.pos++
		case c == '\n':
			p.pos++
			p.finish(false)
			p.readHeredocs()
		case c == '#' && p.atWordStart():
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		case c == ';':
			p.pos++
			p.finish(false)
		case c == '|':
			p.pos++
			if p.peek('|') {
				p.pos++
				p.finish(false)
				continue
			}
			if p.peek('&') {
				p.pos++
			}
			p.finish(true)
		case c == '&' && p.peekAt(1, '>'):
			p.readRedirect()
		case c == '&':
			p.pos++
			if p.peek('&') {
				p.pos++
			}
			p.finish(false)
		case c == '(':
			p.pos++
			p.finish(false)
			p.subshells++
			p.steps = append(p.steps, step{kind: stepEnter})
		case c == ')':
			p.pos++
			p.finish(false)
			if p.subshells > 0 {
				p.subshells--
				p.steps = append(p.steps, step{kind: stepLeave})
			}
		case c == '<' || c == '>':
			p.readRedirect()
		case isDigit(c) && p.fdRedirect():
			for isDigit(p.src[p.pos]) {
				p.pos++
			}
			p.readRedirect()
		default:
			w := p.readWord()
			p.current().words = append(p.current().words, w)
		}
	}
	p.finish(false)
}

func (p *parser) peek(c byte) bool {
	return p.peekAt(0, c)
}

func (p *parser) peekAt(offset int, c byte) bool {
	return p.pos+offset < len(p.src) && p.src[p.pos+offset] == c
}

// atWordStart は # がコメントの始まりになる位置（語の途中でない）かを返す。
func (p *parser) atWordStart() bool {
	return p.pos == 0 || strings.IndexByte(" \t\n;&|()", p.src[p.pos-1]) >= 0
}

// fdRedirect は、現在の位置の数字の並びの直後に < か > が続く（2>file のような fd つきのリダイレクト）かを返す。
func (p *parser) fdRedirect() bool {
	if !p.atWordStart() {
		return false
	}
	index := p.pos
	for index < len(p.src) && isDigit(p.src[index]) {
		index++
	}
	return index < len(p.src) && (p.src[index] == '<' || p.src[index] == '>')
}

func (p *parser) current() *command {
	if p.cur == nil {
		p.cur = &command{pipedFrom: p.piped}
		p.piped = nil
	}
	return p.cur
}

// finish は現在のコマンドを閉じる。pipe なら次のコマンドの stdin をこのコマンドにつなぐ。
func (p *parser) finish(pipe bool) {
	if p.cur != nil && (len(p.cur.words) > 0 || len(p.cur.redirects) > 0) {
		p.steps = append(p.steps, step{kind: stepCommand, command: p.cur})
		if pipe {
			p.piped = p.cur
		}
	}
	p.cur = nil
}

// redirectOps は長い順に並べた演算子である（< と > の後ろだけを見る。&> と &>> は別に扱う）。
var redirectOps = []string{"&>>", "&>", "<<<", "<<-", "<<", "<>", "<&", "<", ">>", ">|", ">&", ">"}

func (p *parser) readRedirect() {
	op := ""
	for _, candidate := range redirectOps {
		if strings.HasPrefix(p.src[p.pos:], candidate) {
			op = candidate
			break
		}
	}
	p.pos += len(op)
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
	r := &redirect{op: op}
	if p.pos < len(p.src) && strings.IndexByte("\n;&|()<>", p.src[p.pos]) < 0 {
		r.target = p.readWord()
	}
	if op == "<<" || op == "<<-" {
		p.pending = append(p.pending, r)
		p.pendingStrip = append(p.pendingStrip, op == "<<-")
	}
	p.current().redirects = append(p.current().redirects, r)
}

// readHeredocs は改行の直後から、保留中のヒアドキュメントの本文を出現順に読む。
// 区切りの行が無いまま入力が尽きたら、残りをすべて本文とする（bash も警告して同じに扱う）。
func (p *parser) readHeredocs() {
	for index, r := range p.pending {
		var body strings.Builder
		for p.pos < len(p.src) {
			end := strings.IndexByte(p.src[p.pos:], '\n')
			line := p.src[p.pos:]
			next := len(p.src)
			if end >= 0 {
				line = p.src[p.pos : p.pos+end]
				next = p.pos + end + 1
			}
			p.pos = next
			if p.pendingStrip[index] {
				line = strings.TrimLeft(line, "\t")
			}
			if line == r.target.text {
				break
			}
			body.WriteString(line)
			body.WriteByte('\n')
		}
		r.body = body.String()
	}
	p.pending, p.pendingStrip = nil, nil
}

// readWord は語を 1 つ読む。クォートを外し、コマンド置換の中身は入れ子のコマンドとして steps に足す。
func (p *parser) readWord() word {
	var w word
	var text strings.Builder
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case strings.IndexByte(" \t\n;&|()<>", c) >= 0:
			w.text = text.String()
			return w
		case c == '\\':
			p.pos++
			if p.pos < len(p.src) {
				if p.src[p.pos] != '\n' {
					text.WriteByte(p.src[p.pos])
				}
				p.pos++
			}
		case c == '\'':
			end := strings.IndexByte(p.src[p.pos+1:], '\'')
			if end < 0 {
				text.WriteString(p.src[p.pos+1:])
				p.pos = len(p.src)
				continue
			}
			text.WriteString(p.src[p.pos+1 : p.pos+1+end])
			p.pos += end + 2
		case c == '"':
			p.pos++
			p.readDoubleQuoted(&text, &w)
		case c == '$':
			p.readDollar(&text, &w, false)
		case c == '`':
			p.readBacktick(&text, &w)
		default:
			if c == '~' && p.pos == start {
				w.tilde = true
			}
			if c == '*' || c == '?' || c == '[' {
				w.glob = true
			}
			if c == '{' {
				w.brace = true
			}
			text.WriteByte(c)
			p.pos++
		}
	}
	w.text = text.String()
	return w
}

// readDoubleQuoted は開きの " の直後から閉じの " までを読む。
func (p *parser) readDoubleQuoted(text *strings.Builder, w *word) {
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch c {
		case '"':
			p.pos++
			return
		case '\\':
			if p.pos+1 < len(p.src) && strings.IndexByte("$`\"\\\n", p.src[p.pos+1]) >= 0 {
				if p.src[p.pos+1] != '\n' {
					text.WriteByte(p.src[p.pos+1])
				}
				p.pos += 2
				continue
			}
			text.WriteByte(c)
			p.pos++
		case '$':
			p.readDollar(text, w, true)
		case '`':
			p.readBacktick(text, w)
		default:
			text.WriteByte(c)
			p.pos++
		}
	}
}

// readDollar は $ で始まる展開を読む。値は決まらないので、元の表記を残して dynamic にする。
func (p *parser) readDollar(text *strings.Builder, w *word, quoted bool) {
	start := p.pos
	p.pos++
	if p.pos >= len(p.src) {
		text.WriteByte('$')
		return
	}
	switch c := p.src[p.pos]; {
	case c == '(':
		end, closed := matchParen(p.src, p.pos)
		inner := p.src[p.pos+1 : end]
		if closed {
			inner = p.src[p.pos+1 : end-1]
		}
		if !strings.HasPrefix(inner, "(") {
			p.nested(inner)
		}
		p.pos = end
	case c == '{':
		end := strings.IndexByte(p.src[p.pos:], '}')
		if end < 0 {
			p.pos = len(p.src)
		} else {
			p.pos += end + 1
		}
	case c == '\'' && !quoted:
		// $'...'（ANSI-C のクォート）。エスケープは主なものだけ戻す。
		p.pos++
		for p.pos < len(p.src) && p.src[p.pos] != '\'' {
			if p.src[p.pos] == '\\' && p.pos+1 < len(p.src) {
				text.WriteString(ansiEscape(p.src[p.pos+1]))
				p.pos += 2
				continue
			}
			text.WriteByte(p.src[p.pos])
			p.pos++
		}
		p.pos++
		return
	case isNameChar(c) || strings.IndexByte("@*#?$!-", c) >= 0:
		if isDigit(c) || !isNameChar(c) {
			p.pos++
		} else {
			for p.pos < len(p.src) && isNameChar(p.src[p.pos]) {
				p.pos++
			}
		}
	default:
		text.WriteByte('$')
		return
	}
	w.dynamic = true
	text.WriteString(p.src[start:min(p.pos, len(p.src))])
}

func ansiEscape(c byte) string {
	switch c {
	case 'n':
		return "\n"
	case 't':
		return "\t"
	default:
		return string(c)
	}
}

// readBacktick は `...` のコマンド置換を読む。
func (p *parser) readBacktick(text *strings.Builder, w *word) {
	start := p.pos
	end := strings.IndexByte(p.src[p.pos+1:], '`')
	if end < 0 {
		p.pos = len(p.src)
	} else {
		p.nested(p.src[p.pos+1 : p.pos+1+end])
		p.pos += end + 2
	}
	w.dynamic = true
	text.WriteString(p.src[start:p.pos])
}

// nested はコマンド置換の中身をサブシェルとして steps に足す。
func (p *parser) nested(inner string) {
	if p.depth >= maxNesting {
		return
	}
	p.steps = append(p.steps, step{kind: stepEnter})
	p.steps = append(p.steps, parseShell(inner, p.depth+1)...)
	p.steps = append(p.steps, step{kind: stepLeave})
}

// matchParen は src[open] の ( に対応する ) の直後の位置を返す。閉じなければ末尾と偽を返す。クォートの中の括弧は数えない。
func matchParen(src string, open int) (int, bool) {
	depth := 0
	for index := open; index < len(src); index++ {
		switch src[index] {
		case '\\':
			index++
		case '\'':
			end := strings.IndexByte(src[index+1:], '\'')
			if end < 0 {
				return len(src), false
			}
			index += end + 1
		case '"':
			for index++; index < len(src) && src[index] != '"'; index++ {
				if src[index] == '\\' {
					index++
				}
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return index + 1, true
			}
		}
	}
	return len(src), false
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func isNameChar(c byte) bool {
	return c == '_' || isDigit(c) || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
