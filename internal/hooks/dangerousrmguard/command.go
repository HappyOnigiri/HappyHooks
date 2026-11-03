package dangerousrmguard

import (
	"strings"

	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

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
