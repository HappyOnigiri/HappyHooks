package irreversibleguard

import (
	"strings"

	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

// secretScanner は 1 つのセグメントの中から、移植元の SECRET_CORE（秘密ファイルのトークン）を探す。
//
// 移植元の正規表現は否定の先読み・後読みを使っており、RE2 では書けない。先読みが失敗すると Python は
// 任意部分を短くしてやり直す（バックトラックする）ので、候補を最長で 1 つ取って確かめるだけでは同じにならない。
// ここでは同じ優先順で候補を試す手書きの照合にし、位置ごとの結果を覚えて入力の長さに比例する時間で終える。
//
//	SECRET_CORE  = (?:\S*/)?(?:_ENV|_ENVRC_LOCAL|_SSH|_PEM)
//	_ENV         = \.env(?:rc)?(?:\.(?!OK(?![\w.-]))[\w-][\w.-]*)?(?![\w.\-/])   OK = example|sample|template|dist
//	_ENVRC_LOCAL = envrc\.local(?![\w.\-/])
//	_SSH         = \.ssh(?:/(?!known_hosts|config(?![\w.-]))[\w.@-]+(?<!\.pub))?/?(?![\w.\-/])
//	_PEM         = [\w.@-]*\.pem(?![\w.\-/])
//
// 位置は rune 単位で数える（Python の str の添字と同じ）。
type secretScanner struct {
	text []rune
	// nameEnd[i] は i から始まる [\w.-] の並びの終わり、pathEnd[i] は [\w.@-] の並びの終わりである。
	nameEnd, pathEnd []int
	// bestSlash[i] は i から始まる \S の並びの中で、直後から秘密ファイルの名前が始まる最も右の / の位置（無ければ -1）である。
	bestSlash []int
	// ends は位置ごとの coreName の結果（未計算は -2、不一致は -1）である。
	ends []int
}

func newSecretScanner(segment string) *secretScanner {
	text := []rune(segment)
	size := len(text)
	scanner := &secretScanner{
		text: text, nameEnd: make([]int, size+1), pathEnd: make([]int, size+1),
		bestSlash: make([]int, size+1), ends: make([]int, size+1),
	}
	scanner.nameEnd[size], scanner.pathEnd[size], scanner.bestSlash[size] = size, size, -1
	for index := size - 1; index >= 0; index-- {
		scanner.nameEnd[index], scanner.pathEnd[index] = index, index
		if isNameChar(text[index]) {
			scanner.nameEnd[index] = scanner.nameEnd[index+1]
		}
		if isNameChar(text[index]) || text[index] == '@' {
			scanner.pathEnd[index] = scanner.pathEnd[index+1]
		}
	}
	for index := range scanner.ends {
		scanner.ends[index] = -2
	}
	for index := size - 1; index >= 0; index-- {
		switch {
		case py.IsSpace(text[index]):
			scanner.bestSlash[index] = -1
		case scanner.bestSlash[index+1] >= 0:
			scanner.bestSlash[index] = scanner.bestSlash[index+1]
		case text[index] == '/' && scanner.coreName(index+1) >= 0:
			scanner.bestSlash[index] = index
		default:
			scanner.bestSlash[index] = -1
		}
	}
	return scanner
}

// isNameChar は [\w.-] である。
func isNameChar(r rune) bool {
	return py.IsWord(r) || r == '.' || r == '-'
}

// endsName は先読み (?![\w.\-/]) が成り立つか（index が末尾か、その文字が [\w.\-/] でないか）を返す。
func (s *secretScanner) endsName(index int) bool {
	return index >= len(s.text) || (!isNameChar(s.text[index]) && s.text[index] != '/')
}

// endsNameStrict は先読み (?![\w.-]) が成り立つかを返す（/ を含まない）。
func (s *secretScanner) endsNameStrict(index int) bool {
	return index >= len(s.text) || !isNameChar(s.text[index])
}

func (s *secretScanner) hasAt(index int, literal string) bool {
	for _, r := range literal {
		if index >= len(s.text) || s.text[index] != r {
			return false
		}
		index++
	}
	return true
}

// core は SECRET_CORE を index から照合し、一致すればその終わりを、しなければ -1 を返す。
// (?:\S*/)? は貪欲なので、右の / から順に試し、どれも駄目なら接頭辞なしで試す。
func (s *secretScanner) core(index int) int {
	if slash := s.bestSlash[index]; slash >= 0 {
		return s.coreName(slash + 1)
	}
	return s.coreName(index)
}

// coreName は _ENV|_ENVRC_LOCAL|_SSH|_PEM をこの順に index から照合する。
func (s *secretScanner) coreName(index int) int {
	if s.ends[index] != -2 {
		return s.ends[index]
	}
	end := s.env(index)
	if end < 0 && s.hasAt(index, "envrc.local") && s.endsName(index+len("envrc.local")) {
		end = index + len("envrc.local")
	}
	if end < 0 {
		end = s.ssh(index)
	}
	if end < 0 {
		end = s.pem(index)
	}
	s.ends[index] = end
	return end
}

// env は _ENV である。rc の有無はこの順に試す。
// 名前の部分 [\w-][\w.-]* は、終わりの先読みがあるので [\w.-] の並びを最後まで取ったときにしか成り立たない。
// 並び全体が OK の語ならその部分は付けられず、付けないと直後の . が先読みで弾かれる。
func (s *secretScanner) env(index int) int {
	if !s.hasAt(index, ".env") {
		return -1
	}
	starts := []int{index + len(".env")}
	if s.hasAt(index+len(".env"), "rc") {
		starts = []int{index + len(".envrc"), index + len(".env")}
	}
	for _, start := range starts {
		if s.hasAt(start, ".") {
			nameStart := start + 1
			nameEnd := s.nameEnd[nameStart]
			if nameEnd > nameStart && s.text[nameStart] != '.' &&
				!envTemplates[string(s.text[nameStart:nameEnd])] && s.endsName(nameEnd) {
				return nameEnd
			}
		}
		if s.endsName(start) {
			return start
		}
	}
	return -1
}

// ssh は _SSH である。/ に続く名前 [\w.@-]+ は長い順に試す。
// 最長でない長さで終わりの先読みが成り立つのは、次の文字が @（[\w.@-] に入り、[\w.\-/] に入らない）のときだけである。
func (s *secretScanner) ssh(index int) int {
	if !s.hasAt(index, ".ssh") {
		return -1
	}
	after := index + len(".ssh")
	nameStart := after + 1
	if s.hasAt(after, "/") && !s.hasAt(nameStart, "known_hosts") &&
		(!s.hasAt(nameStart, "config") || !s.endsNameStrict(nameStart+len("config"))) {
		longest := s.pathEnd[nameStart]
		for end := longest; end > nameStart; end-- {
			if end < longest && s.text[end] != '@' {
				continue
			}
			if end >= 4 && string(s.text[end-4:end]) == ".pub" {
				continue
			}
			if result := s.optionalSlash(end); result >= 0 {
				return result
			}
		}
	}
	return s.optionalSlash(after)
}

// optionalSlash は `/?(?![\w.\-/])` を index から照合する。/ は貪欲に取る。
func (s *secretScanner) optionalSlash(index int) int {
	if s.hasAt(index, "/") && s.endsName(index+1) {
		return index + 1
	}
	if s.endsName(index) {
		return index
	}
	return -1
}

// pem は _PEM である。[\w.@-]* は貪欲なので、並びの中の .pem を右から順に試す。
func (s *secretScanner) pem(index int) int {
	for start := s.pathEnd[index] - len(".pem"); start >= index; start-- {
		if s.hasAt(start, ".pem") && s.endsName(start+len(".pem")) {
			return start + len(".pem")
		}
	}
	return -1
}

// isPrefixChar は _PRE（トークンの直前に許す文字 [\s'"=(/>]）である。
// > が要るのは空白なしのリダイレクト（`echo x >.env`）を拾うためである。
func isPrefixChar(r rune) bool {
	return py.IsSpace(r) || strings.ContainsRune(`'"=(/>`, r)
}

// tokens は `re.finditer(_PRE(SECRET_CORE))` の各一致のトークンを、移植元と同じ順に返す。
func (s *secretScanner) tokens() []string {
	var found []string
	position := 0
	for {
		start, end := s.nextToken(position)
		if start < 0 {
			return found
		}
		found = append(found, string(s.text[start:end]))
		position = end
	}
}

// nextToken は position 以降で最初に一致するトークンの範囲を返す。
// 一致の開始は _PRE の位置で、`^` は文字列の先頭でだけ、文字クラスより先に試す。
func (s *secretScanner) nextToken(position int) (start, end int) {
	if position == 0 {
		if end := s.core(0); end >= 0 {
			return 0, end
		}
	}
	for index := position; index < len(s.text); index++ {
		if !isPrefixChar(s.text[index]) {
			continue
		}
		if end := s.core(index + 1); end >= 0 {
			return index + 1, end
		}
	}
	return -1, -1
}

// quotedCoreAt は `['"]?{SECRET_CORE}` が index から一致するかを返す。
func (s *secretScanner) quotedCoreAt(index int) bool {
	if index >= len(s.text) {
		return false
	}
	if s.core(index) >= 0 {
		return true
	}
	return (s.text[index] == '\'' || s.text[index] == '"') && s.core(index+1) >= 0
}

// skipSpaces は index から空白を読み飛ばした位置を返す。
func (s *secretScanner) skipSpaces(index int) int {
	for index < len(s.text) && py.IsSpace(s.text[index]) {
		index++
	}
	return index
}

// commandAt は index から語 name が始まり、直後が空白で、直前が `(?:^|[\s(])` の境界（allowPath なら / も）かを返す。
// PATH_PREFIX の \S*/ は、直前が / なら必ず境界（先頭か空白か ( ）まで遡れるので、「直前が / 」と同じになる。
func (s *secretScanner) commandAt(index int, name string, allowPath bool) bool {
	if !s.hasAt(index, name) || index+len(name) >= len(s.text) || !py.IsSpace(s.text[index+len(name)]) {
		return false
	}
	if index == 0 {
		return true
	}
	before := s.text[index-1]
	return py.IsSpace(before) || before == '(' || (allowPath && before == '/')
}

// argumentReaches は、コマンド名の直後の空白から、ハイフンで始まる引数の並びを飛ばした先の引数のどれかが
// `['"]?{SECRET_CORE}` に一致するかを返す。minDash はハイフンの後ろに要る \S の数（-\S+ なら 1、-\S* なら 0）である。
// 空白の途中から照合を始めてもトークンは一致しないので、各引数の先頭だけを試せばよい。
func (s *secretScanner) argumentReaches(index, minDash int) bool {
	for {
		index = s.skipSpaces(index)
		if s.quotedCoreAt(index) {
			return true
		}
		if index >= len(s.text) || s.text[index] != '-' {
			return false
		}
		end := index + 1
		for end < len(s.text) && !py.IsSpace(s.text[end]) {
			end++
		}
		// ハイフンの引数の後ろには空白が要る。
		if end-index-1 < minDash || end >= len(s.text) {
			return false
		}
		index = end
	}
}

// movesSecret は `(?:^|[\s(]){PATH_PREFIX}mv\s+(?:-\S+\s+)*['"]?{SECRET_CORE}` である。
func (s *secretScanner) movesSecret() bool {
	for index := range s.text {
		if s.commandAt(index, "mv", true) && s.argumentReaches(index+len("mv"), 1) {
			return true
		}
	}
	return false
}

// teesToSecret は `(?:^|[\s(])tee\s+(?:-\S*\s+)*['"]?{SECRET_CORE}` である。
func (s *secretScanner) teesToSecret() bool {
	for index := range s.text {
		if s.commandAt(index, "tee", false) && s.argumentReaches(index+len("tee"), 0) {
			return true
		}
	}
	return false
}

// redirectsToSecret は `(?<!>)>\s*['"]?{SECRET_CORE}`（>> を除く上書きのリダイレクト）である。
func (s *secretScanner) redirectsToSecret() bool {
	for index, r := range s.text {
		if r != '>' || (index > 0 && s.text[index-1] == '>') {
			continue
		}
		if s.quotedCoreAt(s.skipSpaces(index + 1)) {
			return true
		}
	}
	return false
}
