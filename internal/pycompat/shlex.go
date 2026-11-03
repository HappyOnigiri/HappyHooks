package pycompat

import (
	"errors"
	"strings"
)

// Python の shlex（posix=True）の字句解析を再現する。
// 使い方（区切りの文字、トークンの解釈）は hook ごとに違うので、ここでは shlex の状態機械だけを持ち、
// どの文字を区切りや空白にするかは呼び出し側が渡す。

// ErrNoClosingQuotation と ErrNoEscapedCharacter は、shlex が送出する ValueError にあたる。
var (
	ErrNoClosingQuotation = errors.New("no closing quotation")
	ErrNoEscapedCharacter = errors.New("no escaped character")
)

const (
	shlexQuotes        = "'\""
	shlexEscapedQuotes = `"`
	shlexEscape        = `\`
	// 状態は Python の shlex と同じ文字で表す。引用やエスケープの中では、その文字そのものが状態になる。
	shlexStateWhitespace   rune = ' '
	shlexStateWord         rune = 'a'
	shlexStatePunctuation  rune = 'c'
	shlexStateEndOfInput   rune = -1
	shlexNoPushbackPending rune = -2
)

// ShlexSplit は Python の shlex.shlex(s, posix=True, punctuation_chars=punctuation) に
// whitespace=whitespace・whitespace_split=True・commenters="" を設定し、list() で読み切った結果を返す。
// 引用やエスケープが閉じないときは、Python と同じく途中までのトークンを捨ててエラーを返す。
//
// shlex.split(s) は ShlexSplit(s, " \t\r\n", "") にあたる。
// punctuation を渡すと、その文字の並びを 1 つのトークンにする（punctuation_chars）。
func ShlexSplit(s, whitespace, punctuation string) ([]string, error) {
	lexer := &shlexLexer{
		input: []rune(s), whitespace: whitespace, punctuation: punctuation,
		state: shlexStateWhitespace, pushback: shlexNoPushbackPending,
	}
	var tokens []string
	for {
		token, ok, err := lexer.readToken()
		if err != nil {
			return nil, err
		}
		if !ok {
			return tokens, nil
		}
		tokens = append(tokens, token)
	}
}

type shlexLexer struct {
	input       []rune
	position    int
	whitespace  string
	punctuation string
	state       rune
	// pushback は punctuation_chars を使うときの _pushback_chars である。押し戻すのは常に 1 文字だけなので 1 つで足りる。
	pushback rune
}

// next は次の文字を返す。入力の終わりなら ok は偽になる（Python の空文字列）。
func (l *shlexLexer) next() (rune, bool) {
	if l.pushback != shlexNoPushbackPending {
		char := l.pushback
		l.pushback = shlexNoPushbackPending
		return char, true
	}
	if l.position >= len(l.input) {
		return 0, false
	}
	char := l.input[l.position]
	l.position++
	return char, true
}

func in(char rune, set string) bool {
	return strings.ContainsRune(set, char)
}

// readToken は shlex.read_token の posix の経路である。ok が偽なら入力の終わり（Python の None）である。
func (l *shlexLexer) readToken() (string, bool, error) {
	quoted := false
	escapedState := shlexStateWhitespace
	var token strings.Builder
	for {
		if l.state == shlexStateEndOfInput {
			return "", false, nil
		}
		char, ok := l.next()
		switch {
		case l.state == shlexStateWhitespace:
			switch {
			case !ok:
				l.state = shlexStateEndOfInput
				return l.finish(&token, quoted)
			case in(char, l.whitespace):
				if token.Len() > 0 || quoted {
					return token.String(), true, nil
				}
			case in(char, shlexEscape):
				escapedState = shlexStateWord
				l.state = char
			case in(char, l.punctuation):
				token.WriteRune(char)
				l.state = shlexStatePunctuation
			case in(char, shlexQuotes):
				l.state = char
			default:
				// whitespace_split が真なので、語の文字以外も語として読み始める。
				token.WriteRune(char)
				l.state = shlexStateWord
			}
		case in(l.state, shlexQuotes):
			quoted = true
			switch {
			case !ok:
				return "", false, ErrNoClosingQuotation
			case char == l.state:
				l.state = shlexStateWord
			case in(char, shlexEscape) && in(l.state, shlexEscapedQuotes):
				escapedState = l.state
				l.state = char
			default:
				token.WriteRune(char)
			}
		case in(l.state, shlexEscape):
			if !ok {
				return "", false, ErrNoEscapedCharacter
			}
			// 引用の中では、引用符そのものとエスケープ文字だけをエスケープできる。
			if in(escapedState, shlexQuotes) && char != l.state && char != escapedState {
				token.WriteRune(l.state)
			}
			token.WriteRune(char)
			l.state = escapedState
		case l.state == shlexStateWord || l.state == shlexStatePunctuation:
			switch {
			case !ok:
				l.state = shlexStateEndOfInput
				return l.finish(&token, quoted)
			case in(char, l.whitespace):
				l.state = shlexStateWhitespace
				if token.Len() > 0 || quoted {
					return token.String(), true, nil
				}
			case l.state == shlexStatePunctuation:
				if in(char, l.punctuation) {
					token.WriteRune(char)
					continue
				}
				l.pushback = char
				l.state = shlexStateWhitespace
				return token.String(), true, nil
			case in(char, shlexQuotes):
				l.state = char
			case in(char, shlexEscape):
				escapedState = shlexStateWord
				l.state = char
			case !in(char, l.punctuation):
				token.WriteRune(char)
			default:
				l.pushback = char
				l.state = shlexStateWhitespace
				if token.Len() > 0 || quoted {
					return token.String(), true, nil
				}
			}
		}
	}
}

// finish は入力の終わりで読みかけのトークンを返す。引用の無い空のトークンは Python の None（終わり）になる。
func (l *shlexLexer) finish(token *strings.Builder, quoted bool) (string, bool, error) {
	if token.Len() == 0 && !quoted {
		return "", false, nil
	}
	return token.String(), true, nil
}

// ShlexQuote は Python の shlex.quote である。
func ShlexQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for index := 0; index < len(s); index++ {
		char := s[index]
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') &&
			strings.IndexByte("%+,-./:=@_", char) < 0 {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
