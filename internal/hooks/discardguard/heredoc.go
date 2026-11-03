package discardguard

import (
	"slices"
	"strings"

	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

// --- here-document ---------------------------------------------------------

// span は command の中の範囲 [start, end) である。
type span struct{ start, end int }

func inSpans(spans []span, offset int) bool {
	return slices.ContainsFunc(spans, func(s span) bool { return s.start <= offset && offset < s.end })
}

// heredoc は 1 行の中の here-document の演算子である。
type heredoc struct {
	delimiter string
	// quoted は区切りの語がクォートされているか（本文を展開しない）を示す。
	quoted bool
	// stripTabs は <<- の形（本文と区切りの行の先頭のタブを除く）を示す。
	stripTabs bool
	// files は、本文をファイルへ書き出すだけの形（cat > file / tee file）で書き出すファイルの名前（パスの最後の要素）である。
	// それ以外の形では空である。
	files []string
}

// stripFileHeredocs は、ファイルへ書き出す here-document の本文（cat > x.sh <<'EOF' ... EOF）を取り除いた command と、
// 残した本文（シェルへ流し込む bash <<'EOF' など）の範囲を返す。書き出した本文はこのコマンドの中では実行されないので、
// 破棄の検出と対象の特定から外す。別の呼び出しでそのスクリプトを実行しても hook には中身が見えないので、書き出しを検出しても保護は強くならない。
// 次の本文は取り除かない（このコマンドの中で実行されうる）。
//   - 書き出したファイルの名前が、本文の外にもう一度現れるもの（cat > run.sh <<'EOF' ... EOF; ./run.sh）。
//   - 区切りがクォートされておらず、$(...) やバッククォートを含むもの（外側のシェルが本文の中で実行する）。
func stripFileHeredocs(command string) (string, []span) {
	if !strings.Contains(command, "<<") {
		return command, nil
	}
	pieces := splitHeredocs(command)
	var outside strings.Builder
	for _, piece := range pieces {
		if !piece.body {
			outside.WriteString(piece.text)
		}
	}
	var builder strings.Builder
	var bodies []span
	for _, piece := range pieces {
		if piece.body {
			operator := piece.operator
			removable := len(operator.files) > 0 &&
				(operator.quoted || !strings.Contains(piece.text, "$(") && !strings.Contains(piece.text, "`")) &&
				!slices.ContainsFunc(operator.files, func(name string) bool { return strings.Count(outside.String(), name) > 1 })
			if removable {
				continue
			}
			start := builder.Len()
			builder.WriteString(piece.text)
			bodies = append(bodies, span{start, builder.Len()})
			continue
		}
		builder.WriteString(piece.text)
	}
	return builder.String(), bodies
}

// heredocPiece は command を here-document の本文とそれ以外に分けた断片である。
type heredocPiece struct {
	text     string
	body     bool
	operator heredoc
}

// splitHeredocs は command を here-document の本文とそれ以外に分ける。本文は区切りの行の直前までで、区切りの行は本文の外に置く。
// 区切りの行が無ければ末尾までを本文とする（bash と同じ）。
func splitHeredocs(command string) []heredocPiece {
	var pieces []heredocPiece
	var quote byte
	var outside strings.Builder
	rest := command
	for rest != "" {
		line, tail, more := strings.Cut(rest, "\n")
		operators := heredocsInLine(line, &quote)
		outside.WriteString(line)
		if !more {
			break
		}
		outside.WriteByte('\n')
		rest = tail
		for _, operator := range operators {
			bodyEnd, after := len(rest), len(rest)
			for position := 0; position < len(rest); {
				candidate, _, hasNext := strings.Cut(rest[position:], "\n")
				compared := candidate
				if operator.stripTabs {
					compared = strings.TrimLeft(candidate, "\t")
				}
				next := position + len(candidate)
				if hasNext {
					next++
				}
				if compared == operator.delimiter {
					bodyEnd, after = position, next
					break
				}
				position = next
			}
			pieces = append(pieces, heredocPiece{text: outside.String()})
			outside.Reset()
			pieces = append(pieces, heredocPiece{text: rest[:bodyEnd], body: true, operator: operator})
			outside.WriteString(rest[bodyEnd:after])
			rest = rest[after:]
		}
	}
	return append(pieces, heredocPiece{text: outside.String()})
}

// heredocsInLine は line の中の here-document の演算子を出現順に返す。quote は行をまたぐクォートの状態である。
func heredocsInLine(line string, quote *byte) []heredoc {
	var operators []heredoc
	// segment は今の単純コマンドの開始位置と、その中の演算子の添字である。
	segmentStart, firstOperator := 0, 0
	finish := func(end int) {
		files := writtenFiles(line, segmentStart, end)
		for index := firstOperator; index < len(operators); index++ {
			operators[index].files = files
		}
		firstOperator = len(operators)
	}
	for index := 0; index < len(line); index++ {
		c := line[index]
		if *quote != 0 {
			if *quote == '"' && c == '\\' {
				index++
			} else if c == *quote {
				*quote = 0
			}
			continue
		}
		switch {
		case c == '\\':
			index++
		case c == '\'' || c == '"':
			*quote = c
		case c == '#' && (index == 0 || py.IsSpace(rune(line[index-1]))):
			// コメントの中の << は演算子ではない。
			finish(index)
			return operators
		case strings.IndexByte(separatorChars+"`", c) >= 0:
			finish(index)
			segmentStart = index + 1
		case c == '<' && strings.HasPrefix(line[index:], "<<") && !strings.HasPrefix(line[index:], "<<<") &&
			(index == 0 || line[index-1] != '<'):
			operator, end, ok := parseHeredoc(line, index+2)
			if ok {
				operators = append(operators, operator)
			}
			index = end - 1
		}
	}
	finish(len(line))
	return operators
}

// parseHeredoc は line[start:]（<< の直後）から区切りの語を読む。end は読み終えた位置である。
func parseHeredoc(line string, start int) (operator heredoc, end int, ok bool) {
	index := start
	if index < len(line) && line[index] == '-' {
		operator.stripTabs = true
		index++
	}
	for index < len(line) && (line[index] == ' ' || line[index] == '\t') {
		index++
	}
	var delimiter strings.Builder
	for index < len(line) {
		c := line[index]
		switch {
		case c == '\'' || c == '"':
			closing := strings.IndexByte(line[index+1:], c)
			if closing < 0 {
				return heredoc{}, len(line), false
			}
			delimiter.WriteString(line[index+1 : index+1+closing])
			operator.quoted = true
			index += closing + 2
			continue
		case c == '\\':
			operator.quoted = true
			if index+1 < len(line) {
				delimiter.WriteByte(line[index+1])
			}
			index += 2
			continue
		case py.IsSpace(rune(c)) || strings.IndexByte(separatorChars+"<>`", c) >= 0:
		default:
			delimiter.WriteByte(c)
			index++
			continue
		}
		break
	}
	operator.delimiter = delimiter.String()
	return operator, min(index, len(line)), operator.delimiter != ""
}

// writtenFiles は line[start:end] の単純コマンドが here-document の本文をファイルへ書き出すだけなら、書き出すファイルの名前を返す。
// cat はファイルへのリダイレクトの先、tee は引数のファイルを書き出し先とみなす。パイプで次のコマンドへ渡す形、
// コマンド置換（$( ... ) / バッククォート）の中、プロセス置換（>( ... )）は、本文が実行されうるので対象外（nil）にする。
func writtenFiles(line string, start, end int) []string {
	if end < len(line) && line[end] == '|' && !strings.HasPrefix(line[end:], "||") {
		return nil
	}
	if start > 0 && line[start-1] == '`' {
		return nil
	}
	if start > 1 && line[start-1] == '(' && strings.IndexByte("$<>", line[start-2]) >= 0 {
		return nil
	}
	segment := line[start:end]
	if strings.Contains(segment, ">(") || strings.Contains(segment, "<(") {
		return nil
	}
	words := py.Fields(segment)
	for len(words) > 0 {
		if _, _, ok := assignment(words[0]); !ok {
			break
		}
		words = words[1:]
	}
	if len(words) == 0 {
		return nil
	}
	var paths []string
	switch words[0] {
	case "tee":
		for _, word := range words[1:] {
			if !strings.HasPrefix(word, "-") && !strings.HasPrefix(word, "<") && !strings.HasPrefix(word, ">") {
				paths = append(paths, word)
			}
		}
	case "cat":
		for index := strings.IndexByte(segment, '>'); index >= 0; {
			target := py.Fields(strings.TrimLeft(segment[index+1:], ">"))
			if len(target) > 0 && !strings.HasPrefix(target[0], "&") {
				paths = append(paths, target[0])
			}
			next := strings.IndexByte(segment[index+1:], '>')
			if next < 0 {
				break
			}
			index += 1 + next
		}
	}
	var files []string
	for _, path := range paths {
		name := strings.Trim(path[strings.LastIndex(path, "/")+1:], `'"`)
		if name == "" || strings.ContainsAny(name, "<>") {
			// ファイルの名前が分からなければ、実行されるかを確かめられない。
			return nil
		}
		files = append(files, name)
	}
	return files
}
