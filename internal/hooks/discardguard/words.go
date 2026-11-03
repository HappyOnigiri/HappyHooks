package discardguard

import (
	"strings"
	"unicode/utf8"

	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

// token は command の 1 語である。
type token struct {
	text string
	// seps は直前の語との間にある区切り文字（`(){};&|` と改行）を出現順に持つ。サブシェルの出入りと、コマンドの境界を辿るために使う。
	seps string
	// offset は command の中での語の先頭の位置である（here-document の本文の中かを判定する）。
	offset int
}

// tokenize は区切り文字を空白に寄せて command をトークンに分ける。区切りと語がくっついた形でも cd / git をトークンとして拾うためである。
// トークンの列は、括弧を空白として読んだ移植元と同じにする（区切りそのものはトークンにしない）。
// 例外として、${NAME} の波括弧は区切りにせず語の一部にする（変数の参照として展開するため）。名前だけを囲む形に限るので、
// ${X:-$(cd ...)} のような中身を 1 語に隠すことはない。
func tokenize(command string) []token {
	var tokens []token
	var seps []byte
	start := -1
	flush := func(end int) {
		if start >= 0 {
			tokens = append(tokens, token{text: command[start:end], seps: string(seps), offset: start})
			seps, start = nil, -1
		}
	}
	for index := 0; index < len(command); {
		r, size := utf8.DecodeRuneInString(command[index:])
		switch {
		case r == '$' && strings.HasPrefix(command[index+1:], "{"):
			if length := bracedNameLength(command[index+2:]); length > 0 {
				if start < 0 {
					start = index
				}
				index += 2 + length + 1
				continue
			}
			if start < 0 {
				start = index
			}
		case strings.ContainsRune(separatorChars, r):
			flush(index)
			seps = append(seps, byte(r))
		case py.IsSpace(r):
			flush(index)
			if r == '\n' {
				seps = append(seps, '\n')
			}
		default:
			if start < 0 {
				start = index
			}
		}
		index += size
	}
	flush(len(command))
	return tokens
}

// separatorChars は tokenize で空白として扱う区切り文字である。
const separatorChars = "(){};&|"

// bracedNameLength は s が NAME} で始まるとき NAME の長さを返し、そうでなければ 0 を返す。
func bracedNameLength(s string) int {
	length := nameLength(s)
	if length == 0 || !strings.HasPrefix(s[length:], "}") {
		return 0
	}
	return length
}

// nameLength は s の先頭にあるシェルの変数名の長さを返す（無ければ 0）。
func nameLength(s string) int {
	for index := 0; index < len(s); index++ {
		c := s[index]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || index > 0 && c >= '0' && c <= '9' {
			continue
		}
		return index
	}
	return len(s)
}

func isNameChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// onlyAndThen は区切りが && だけ（括弧・波括弧と && の後の改行を除く）か、区切りが無い（同じコマンドの中）かを返す。
func onlyAndThen(seps string) bool {
	rest := strings.Map(func(r rune) rune {
		if strings.ContainsRune("(){}", r) {
			return -1
		}
		return r
	}, seps)
	return rest == "" || strings.HasPrefix(rest, "&&") && strings.Trim(rest[2:], "\n") == ""
}

// keywordsBeforeCommand の直後の語はコマンドの位置にある（then D=x の D=x は代入として読む）。
var keywordsBeforeCommand = []string{"then", "do", "else", "elif", "if", "while", "until", "!"}

// blockOpeners と blockClosers は、中のコマンドが条件つきでしか実行されない複合コマンドの開始と終了である。
var (
	blockOpeners = []string{"if", "case", "for", "select", "while", "until", "function"}
	blockClosers = []string{"fi", "esac", "done"}
)

// assignment は NAME=VALUE の形の語を分ける。
func assignment(word string) (name, value string, ok bool) {
	length := nameLength(word)
	if length == 0 || !strings.HasPrefix(word[length:], "=") {
		return "", "", false
	}
	return word[:length], word[length+1:], true
}

// assignmentStandsAlone は、tokens[index] から続く代入の並びの後にコマンドが続かず（D=x git ... は git の環境だけを変える）、
// パイプやバックグラウンド（サブシェルで走るので、シェルの変数を変えない）で終わらないかを返す。
func assignmentStandsAlone(tokens []token, index int) bool {
	next := index + 1
	for next < len(tokens) && tokens[next].seps == "" {
		if _, _, ok := assignment(tokens[next].text); !ok {
			return false
		}
		next++
	}
	return next >= len(tokens) || !hasLonePipeOrAmpersand(tokens[next].seps)
}

// hasLonePipeOrAmpersand は区切りが && / || ではない | か & を含むかを返す。
func hasLonePipeOrAmpersand(seps string) bool {
	for index := 0; index < len(seps); index++ {
		c := seps[index]
		if c != '&' && c != '|' {
			continue
		}
		if index+1 < len(seps) && seps[index+1] == c {
			index++
			continue
		}
		return true
	}
	return false
}

// errexitFlag は set の引数 words[start:] から errexit（-e / -o errexit、-euo pipefail のような連結）の指定を読む。
// ok が偽なら指定が無い。
func errexitFlag(words []string, start int) (on, ok bool) {
	for index := start; index < len(words); index++ {
		word := words[index]
		if len(word) < 2 || word[0] != '-' && word[0] != '+' || word == "--" || nameLength(word[1:]) != len(word)-1 {
			break
		}
		enable := word[0] == '-'
		if strings.ContainsRune(word[1:], 'e') {
			on, ok = enable, true
		}
		if strings.ContainsRune(word[1:], 'o') && index+1 < len(words) {
			// -o の値（オプションの名前）を読む。
			index++
			if words[index] == "errexit" {
				on, ok = enable, true
			}
		}
	}
	return on, ok
}

// trustedNames は、コマンドの中で 1 回だけ名前として現れる変数を返す。$NAME / ${NAME} の参照は数えない。
// 代入の他に名前が現れれば（for D in、read D、unset D、D+=x、2 回目の代入、${D:=x} など）値が変わりうるので信用しない。
// クォートの中やパスの一部でも数えるので多めに数えるが、多めに数えても特定できないものが増えるだけである。
func trustedNames(command string) map[string]bool {
	counts := map[string]int{}
	for index := 0; index < len(command); {
		length := nameLength(command[index:])
		if length == 0 || index > 0 && isNameChar(command[index-1]) {
			index++
			continue
		}
		name := command[index : index+length]
		end := index + length
		before, after := byte(' '), byte(' ')
		if index > 0 {
			before = command[index-1]
		}
		if end < len(command) {
			after = command[end]
		}
		switch {
		case before == '{' && index >= 2 && command[index-2] == '$':
			// ${NAME} は参照である。${NAME:=x} のように後ろが続く形は代入しうるので数える。
			if after != '}' {
				counts[name]++
			}
		case before == '$' || strings.IndexByte(notMentionBefore, before) >= 0:
		case strings.IndexByte(notMentionAfter, after) >= 0:
		default:
			counts[name]++
		}
		index = end
	}
	trusted := map[string]bool{}
	for name, count := range counts {
		trusted[name] = count == 1
	}
	return trusted
}

// notMentionBefore と notMentionAfter は、直前・直後にあれば変数への書き込みになりえない文字である（パスやフラグの一部）。
const (
	notMentionBefore = "/.-:=@%+,^#~"
	notMentionAfter  = "/.:,@#~{"
)

// expandPath は cd / git -C の引数を展開する。展開できなければ ok は偽になる。
// 先頭の ~ / ~/ だけのパスは移植元と同じく展開し、変数やクォートを含むものは vars（nil なら展開しない）で expandWord する。
func expandPath(word string, vars map[string]string) (string, bool) {
	path := word
	if path == "~" || strings.HasPrefix(path, "~/") {
		path = expandUser(path)
	}
	if isStaticPath(path) {
		return path, true
	}
	if vars == nil {
		return "", false
	}
	return expandWord(word, vars, false)
}

// expandWord は語の中の $NAME / ${NAME} を vars の値で、クォートを中身で置き換える。展開できなければ ok は偽になる。
// クォートの外の値は、シェルが空白で分割し glob を展開するので、空白や glob の文字を含めば展開できないものとする（代入の右辺は分割しない）。
// 空の値も展開できないものとする（cd $EMPTY はホームへ移り、git -C "" は今のディレクトリを指す）。
// 先頭の ~ / ~/ はシェルと同じく展開する。バッククォート・$(...)・エスケープ・特殊変数は追わない。
func expandWord(word string, vars map[string]string, assigned bool) (string, bool) {
	var builder strings.Builder
	index := 0
	if word == "~" || strings.HasPrefix(word, "~/") {
		home := expandUser(word[:min(len(word), 2)])
		if strings.HasPrefix(home, "~") {
			return "", false
		}
		builder.WriteString(home)
		index = min(len(word), 2)
	}
	reference := func(quoted bool) bool {
		name := word[index+1:]
		length := nameLength(name)
		consumed := 1 + length
		if strings.HasPrefix(name, "{") {
			length = bracedNameLength(name[1:])
			name, consumed = name[1:], 3+length
		}
		if length == 0 {
			return false
		}
		value, ok := vars[name[:length]]
		if !ok || value == "" || !quoted && !assigned && strings.ContainsFunc(value, splitsOrGlobs) {
			return false
		}
		builder.WriteString(value)
		index += consumed
		return true
	}
	for index < len(word) {
		switch c := word[index]; c {
		case '\'':
			end := strings.IndexByte(word[index+1:], '\'')
			if end < 0 {
				return "", false
			}
			builder.WriteString(word[index+1 : index+1+end])
			index += end + 2
		case '"':
			index++
			for {
				if index >= len(word) {
					return "", false
				}
				c := word[index]
				if c == '"' {
					index++
					break
				}
				if c == '$' {
					if !reference(true) {
						return "", false
					}
					continue
				}
				if c == '`' || c == '\\' {
					return "", false
				}
				builder.WriteByte(c)
				index++
			}
		case '$':
			if !reference(false) {
				return "", false
			}
		default:
			if strings.IndexByte(unquotedUnsupported, c) >= 0 {
				return "", false
			}
			builder.WriteByte(c)
			index++
		}
	}
	return builder.String(), true
}

// unquotedUnsupported はクォートの外にあれば展開を追わない文字である（glob・ブレース展開・エスケープ・リダイレクトなど）。
const unquotedUnsupported = "`*?[]{}\\~&;|()<>! \t\n"

func splitsOrGlobs(r rune) bool {
	return py.IsSpace(r) || strings.ContainsRune("*?[", r)
}
