package dangerousrmguard

import (
	"strings"
	"unicode/utf8"
)

// expansionHeadParses は組み込みの `Pe` のうち、`${` の直後（i）から名前と添字までを読み、続く文字で `}` に届くかを返す。
// 演算子（`:`・`#`・`%`・`/`・`^`・`,`・`-`・`=`・`?`・`+`）が続けば、その後ろは読み切れるものとみなす（通す側）。
// 添字の `]` が見つからないときも通す側に倒す。
//
//nolint:cyclop // bash の字句を 1 文字ずつ読む状態機械で、分岐は文字の種類ごとの規則に対応する。分けると状態の受け渡しが増え、判定が変わり得る。
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
