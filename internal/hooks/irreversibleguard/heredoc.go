package irreversibleguard

import (
	"slices"
	"strings"

	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

// --- ファイルへ書き出すだけの here-document ------------------------------------------

// 移植元との違い: 移植元は here-document の本文もコマンドとして判定していた。cat > x.sh <<'EOF' ... EOF で
// 書き出すだけの本文はこのコマンドの中では実行されないのに、本文にある秘密ファイルへのリダイレクトなどで止まっていた。
// 次の条件をすべて満たす本文だけを判定から外す。満たさなければ従来どおり本文も判定する。
//   - 本文を受けるのが、ファイルへ書き出すだけの cat（> か >> の先がある）か tee（ファイルの引数がある）である。
//   - 区切りの語がクォートされているか、本文が $(...) もバッククォートも含まない（外側のシェルが本文の中で実行しない）。
//   - 書き出すファイルの名前（パスの最後の要素）が、本文の外にもう一度現れない（同じコマンドで実行する形ではない）。
//   - 区切りの行が見つかり、引用がすべて閉じている（本文の範囲を取り違えて、後ろのコマンドまで外さない）。
//
// 書き出し先そのもの（cat > ~/.ssh/config <<'EOF' の ~/.ssh/config）は本文の外に残るので、従来どおり判定する。
// discard-guard にも同じ考えの処理があるが、hook ごとに誤爆の条件を調整するため共通化しない（AGENTS.md）。

// writtenHeredoc は 1 つの here-document の演算子である。
type writtenHeredoc struct {
	delimiter string
	quoted    bool
	stripTabs bool
	// files は本文を書き出すファイルの名前（パスの最後の要素）である。ファイルへ書き出すだけの形でなければ空である。
	files []string
}

// stripWrittenHeredocs は、ファイルへ書き出すだけの here-document の本文を command から取り除く。
func stripWrittenHeredocs(command string) string {
	if !strings.Contains(command, "<<") {
		return command
	}
	lines := strings.SplitAfter(command, "\n")
	type body struct {
		operator writtenHeredoc
		start    int // 本文の最初の行
		end      int // 区切りの行（本文の外）
	}
	var bodies []body
	var quote byte
	for index := 0; index < len(lines); index++ {
		operators := heredocOperators(lines[index], &quote)
		next := index + 1
		for _, operator := range operators {
			end := next
			for end < len(lines) && !isDelimiterLine(lines[end], operator) {
				end++
			}
			if end >= len(lines) {
				// 区切りの行が無い。本文の範囲を確かめられないので何も外さない。
				return command
			}
			bodies = append(bodies, body{operator, next, end})
			next = end + 1
		}
		index = next - 1
	}
	if quote != 0 || len(bodies) == 0 {
		return command
	}
	inBody := make([]bool, len(lines))
	for _, b := range bodies {
		for line := b.start; line < b.end; line++ {
			inBody[line] = true
		}
	}
	var outside strings.Builder
	for index, line := range lines {
		if !inBody[index] {
			outside.WriteString(line)
		}
	}
	remove := make([]bool, len(lines))
	for _, b := range bodies {
		text := strings.Join(lines[b.start:b.end], "")
		if len(b.operator.files) == 0 ||
			!b.operator.quoted && (strings.Contains(text, "$(") || strings.Contains(text, "`")) ||
			slices.ContainsFunc(b.operator.files, func(name string) bool {
				return strings.Count(outside.String(), name) > 1
			}) {
			continue
		}
		for line := b.start; line < b.end; line++ {
			remove[line] = true
		}
	}
	var result strings.Builder
	for index, line := range lines {
		if !remove[index] {
			result.WriteString(line)
		}
	}
	return result.String()
}

// isDelimiterLine は line が operator の区切りの行かを返す。
func isDelimiterLine(line string, operator writtenHeredoc) bool {
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if operator.stripTabs {
		line = strings.TrimLeft(line, "\t")
	}
	return line == operator.delimiter
}

// heredocOperators は line の中の、クォートの外にある here-document の演算子を出現順に返す。
// quote は行をまたぐクォートの状態である（本文の行では呼ばない）。
func heredocOperators(line string, quote *byte) []writtenHeredoc {
	var operators []writtenHeredoc
	segmentStart := 0
	for index := 0; index < len(line); index++ {
		if next, quoted := skipQuoted(line, index, quote); quoted {
			index = next
			continue
		}
		char := line[index]
		switch char {
		case '\\':
			index++
		case '\'', '"':
			*quote = char
		case ';', '&', '|', '(', ')':
			segmentStart = index + 1
		case '<':
			if !strings.HasPrefix(line[index:], "<<") || strings.HasPrefix(line[index:], "<<<") {
				continue
			}
			operator, end, ok := parseWrittenHeredoc(line, index)
			if !ok {
				continue
			}
			operator.files = writtenFiles(line[segmentStart:index] + " " + line[end:segmentEnd(line, end)])
			operators = append(operators, operator)
			index = end - 1
		}
	}
	return operators
}

// segmentEnd は start から、クォートの外の次のコマンドの区切りまでの終わりの位置を返す。
func segmentEnd(line string, start int) int {
	var quote byte
	for index := start; index < len(line); index++ {
		if next, quoted := skipQuoted(line, index, &quote); quoted {
			index = next
			continue
		}
		switch char := line[index]; char {
		case '\\':
			index++
		case '\'', '"':
			quote = char
		case ';', '&', '|', '(', ')', '\n':
			return index
		}
	}
	return len(line)
}

// skipQuoted は、クォートの中なら line[index] を読んでクォートの状態を進め、最後に読んだ位置と真を返す。
// クォートの外なら index と偽を返す。
func skipQuoted(line string, index int, quote *byte) (int, bool) {
	switch *quote {
	case '\'':
		if line[index] == '\'' {
			*quote = 0
		}
	case '"':
		switch line[index] {
		case '\\':
			index++
		case '"':
			*quote = 0
		}
	default:
		return index, false
	}
	return index, true
}

// parseWrittenHeredoc は line[start:] の `<<` から演算子と区切りの語を読み、演算子の直後の位置を返す。
func parseWrittenHeredoc(line string, start int) (operator writtenHeredoc, end int, ok bool) {
	index := start + 2
	if index < len(line) && line[index] == '-' {
		operator.stripTabs = true
		index++
	}
	for index < len(line) && (line[index] == ' ' || line[index] == '\t') {
		index++
	}
	var word strings.Builder
	for index < len(line) {
		char := line[index]
		switch {
		case char == '\'' || char == '"':
			closing := strings.IndexByte(line[index+1:], char)
			if closing < 0 {
				return writtenHeredoc{}, 0, false
			}
			word.WriteString(line[index+1 : index+1+closing])
			operator.quoted = true
			index += closing + 2
			continue
		case char == '\\':
			operator.quoted = true
			index++
			if index < len(line) {
				word.WriteByte(line[index])
				index++
			}
			continue
		case strings.IndexByte(" \t\r\n;&|<>()", char) >= 0:
		default:
			word.WriteByte(char)
			index++
			continue
		}
		break
	}
	if word.Len() == 0 {
		return writtenHeredoc{}, 0, false
	}
	operator.delimiter = word.String()
	return operator, index, true
}

// writtenFiles は here-document を受けるコマンド（演算子を除いた断片）が、本文をファイルへ書き出すだけの
// cat か tee なら、書き出すファイルの名前（パスの最後の要素）を返す。それ以外は空を返す。
func writtenFiles(segment string) []string {
	tokens, err := py.ShlexSplit(segment, " \t\r\n", "")
	if err != nil || len(tokens) == 0 {
		return nil
	}
	name := tokens[0]
	if slash := strings.LastIndexByte(name, '/'); slash >= 0 {
		name = name[slash+1:]
	}
	var files []string
	switch name {
	case "cat":
		for index := 1; index < len(tokens); index++ {
			token := tokens[index]
			path := ""
			switch {
			case token == ">" || token == ">>":
				if index+1 < len(tokens) {
					path = tokens[index+1]
					index++
				}
			case strings.HasPrefix(token, ">>"):
				path = token[2:]
			case strings.HasPrefix(token, ">"):
				path = token[1:]
			}
			if path != "" {
				files = append(files, path)
			}
		}
	case "tee":
		for _, token := range tokens[1:] {
			if !strings.HasPrefix(token, "-") && !strings.HasPrefix(token, ">") {
				files = append(files, token)
			}
		}
	}
	for index, path := range files {
		files[index] = path[strings.LastIndexByte(path, '/')+1:]
	}
	return slices.DeleteFunc(files, func(name string) bool { return name == "" })
}
