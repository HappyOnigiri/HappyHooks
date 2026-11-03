package generatededitguard

import (
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

// 書き込み先の候補は「多めに拾い、マーカーで絞る」。候補が実在する生成ファイルでなければ判定は変わらないので、
// 迷ったら候補に入れる。外部のスクリプトファイル（python3 tools/x.py、go run、make）の中の書き込みは追わない（利用者の決定）。

const (
	// maxGlobMatches は glob 1 つから候補にするパスの上限である。
	maxGlobMatches = 256
	// maxPatchFileSize はパッチのファイルを読む上限である。
	maxPatchFileSize = 1 << 20
)

// declared はコマンドの中の宣言（hhx allow-generated-edit）を解決したものである。
type declared struct {
	reason string
	paths  []string
	// unresolved は静的に決まらず記録できなかったパスの表記である（変数やコマンド置換を含む）。
	unresolved []string
}

// bashScan はコマンド文字列を作業ディレクトリを辿りながら走査し、書き込み先の候補と宣言を集める。
type bashScan struct {
	cwd string
	// outer はサブシェルに入る前の作業ディレクトリ、dirs は pushd で積んだ作業ディレクトリである。
	outer, dirs  []string
	paths        []string
	seen         map[string]bool
	declarations []declared
	// direct は cp・mv・install 以外の書き込みで候補にしたパスである。copies はコピーの書き込み先ごとの送り元である。
	// 送り元がすべて生成ファイルか生成器の出力なら、作り直した結果を置く正規の形として通す（direct にあれば通さない）。
	direct  map[string]bool
	copies  map[string][]string
	outputs map[string]bool
}

// scanBash は command の書き込み先の候補（絶対パス）と宣言を返す。cwd は走査を始める作業ディレクトリである。
func scanBash(command, cwd string) *bashScan {
	scan := &bashScan{
		cwd: cwd, seen: map[string]bool{}, direct: map[string]bool{}, copies: map[string][]string{},
		outputs: map[string]bool{},
	}
	scan.walk(parseShell(command, 0), 0)
	return scan
}

func (s *bashScan) walk(steps []step, depth int) {
	for _, current := range steps {
		switch current.kind {
		case stepEnter:
			s.outer = append(s.outer, s.cwd)
		case stepLeave:
			if len(s.outer) > 0 {
				s.cwd = s.outer[len(s.outer)-1]
				s.outer = s.outer[:len(s.outer)-1]
			}
		case stepCommand:
			s.command(current.command, depth)
		}
	}
}

func (s *bashScan) add(paths ...string) {
	for _, path := range paths {
		s.direct[path] = true
		s.record(path)
	}
}

// record は候補に足す。重複は map で除く（候補が多い入力でも 2 乗の時間にしない）。
func (s *bashScan) record(path string) {
	if !s.seen[path] {
		s.seen[path] = true
		s.paths = append(s.paths, path)
	}
}

// addCopy はコピーの書き込み先を、送り元とともに候補に足す。
func (s *bashScan) addCopy(destination string, sources []string) {
	s.copies[destination] = append(s.copies[destination], sources...)
	s.record(destination)
}

// copiedFromGenerated は path がコピーでだけ書かれ、送り元がすべて生成器の出力か generated を満たすファイルかを返す。
func (s *bashScan) copiedFromGenerated(path string, generated func(string) bool) bool {
	sources, ok := s.copies[path]
	if !ok || s.direct[path] || len(sources) == 0 {
		return false
	}
	for _, source := range sources {
		if !s.outputs[source] && !generated(source) {
			return false
		}
	}
	return true
}

// addWords は語をパスとして解決し、候補に足す。
func (s *bashScan) addWords(words []word) {
	for _, w := range words {
		s.add(s.resolve(w)...)
	}
}

// resolve は語を作業ディレクトリから解決する。値が静的に決まらない語は解決しない。
// glob はファイルシステムで展開する（sed -i ... gen/*.go の対象を拾うため）。
func (s *bashScan) resolve(w word) []string {
	if w.dynamic || w.text == "" {
		return nil
	}
	text := w.text
	if w.tilde && (text == "~" || strings.HasPrefix(text, "~/")) {
		home := py.Home()
		if home == "~" {
			return nil
		}
		text = home + text[1:]
	}
	texts := []string{text}
	if w.brace {
		texts = braceExpand(text, maxGlobMatches)
	}
	var out []string
	for _, expanded := range texts {
		path := s.absolute(expanded)
		if !w.glob {
			out = append(out, path)
			continue
		}
		matches, err := filepath.Glob(path)
		if err != nil || len(matches) == 0 {
			out = append(out, path)
			continue
		}
		out = append(out, matches[:min(len(matches), maxGlobMatches)]...)
	}
	return out
}

// braceExpand はシェルのブレース展開（a{b,c}d → abd acd）を、結果が limit 件を超えない範囲で行う。
// カンマを含まない {} はそのまま残す。{1..3} の範囲は展開しない。
func braceExpand(text string, limit int) []string {
	open := -1
	depth := 0
	for index := 0; index < len(text); index++ {
		switch text[index] {
		case '{':
			if depth == 0 {
				open = index
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth != 0 {
				continue
			}
			parts := splitTopLevel(text[open+1 : index])
			if len(parts) < 2 {
				// カンマの無い括弧は展開しない。後ろにある括弧を展開する。
				rest := braceExpand(text[index+1:], limit)
				out := make([]string, 0, len(rest))
				for _, tail := range rest {
					out = append(out, text[:index+1]+tail)
				}
				return out
			}
			var out []string
			for _, part := range parts {
				for _, expanded := range braceExpand(text[:open]+part+text[index+1:], limit) {
					if len(out) >= limit {
						return out
					}
					out = append(out, expanded)
				}
			}
			return out
		}
	}
	return []string{text}
}

// splitTopLevel は括弧の外のカンマで区切る。
func splitTopLevel(text string) []string {
	var parts []string
	depth, start := 0, 0
	for index := 0; index < len(text); index++ {
		switch text[index] {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, text[start:index])
				start = index + 1
			}
		}
	}
	return append(parts, text[start:])
}

func (s *bashScan) absolute(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(s.cwd, path)
}

// writeRedirects はファイルへ書き込むリダイレクトの演算子である。>& は行き先が fd の番号か - でなければファイルへ書く。
var writeRedirects = map[string]bool{">": true, ">>": true, ">|": true, "&>": true, "&>>": true, "<>": true}

// handWriters は、出力をリダイレクトすれば手で書いた内容になるコマンドである。リダイレクトの行き先はこれらのときだけ調べる。
// 生成器やスクリプトの出力のリダイレクト（mockgen ... > x_mock.go、go run ./cmd/gen > gen.go）は作り直しの正規の形なので止めない。
// 複合コマンドの終わり（} done fi esac）は中身が分からないので、手書きとみなす。
var handWriters = map[string]bool{
	"echo": true, "printf": true, "cat": true, "tac": true, "sed": true, "gsed": true, "awk": true, "gawk": true,
	"mawk": true, "nawk": true, "head": true, "tail": true, "sort": true, "uniq": true, "cut": true, "tr": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "jq": true, "yq": true, "envsubst": true, "base64": true,
	"xxd": true, "column": true, "paste": true, "fmt": true, "fold": true, "rev": true, "nl": true, "yes": true,
	"true": true, "false": true, ":": true, "printenv": true, "tee": true, "dd": true, "iconv": true, "expand": true,
	"unexpand": true, "}": true, "done": true, "fi": true, "esac": true,
}

//nolint:cyclop // bash の字句を 1 文字ずつ読む状態機械で、分岐は文字の種類ごとの規則に対応する。分けると状態の受け渡しが増え、判定が変わり得る。
func (s *bashScan) command(c *command, depth int) {
	args := stripPrefixes(c.words)
	name := ""
	if len(args) > 0 {
		name = baseName(args[0].text)
	}
	inline := false
	if kind := interpreterKind(name); kind != "" {
		if (kind == "perl" || kind == "ruby") && inPlace(args, inPlaceValued[kind]) {
			s.addWords(scriptFiles(args))
		}
		inline = s.inlineCode(kind, c, args)
	}
	if awkNames[name] && awkInPlace(args) {
		s.addWords(awkFiles(args))
	}
	// コマンドの無いリダイレクト（> gen.go）はファイルを空にするので、手書きと同じに扱う。
	// それ以外のコマンド（生成器やスクリプト）の出力の行き先は、コピーの送り元として覚える。
	hand := len(args) == 0 || handWriters[name] || inline
	for _, r := range c.redirects {
		if !writeRedirects[r.op] && (r.op != ">&" || isFD(r.target.text)) {
			continue
		}
		if hand {
			s.addWords([]word{r.target})
			continue
		}
		for _, path := range s.resolve(r.target) {
			s.outputs[path] = true
		}
	}
	if len(args) == 0 {
		return
	}
	switch {
	case name == "cd" || name == "pushd":
		s.changeDirectory(args, name == "pushd")
	case name == "popd":
		if len(s.dirs) > 0 {
			s.cwd = s.dirs[len(s.dirs)-1]
			s.dirs = s.dirs[:len(s.dirs)-1]
		}
	case name == "sed" || name == "gsed":
		if sedInPlace(args) {
			s.addWords(operands(args, map[string]bool{"-e": true, "-f": true, "-l": true, "--expression": true, "--file": true}))
		}
	case name == "tee" || name == "sponge":
		s.addWords(operands(args, nil))
	case name == "cp" || name == "mv" || name == "install" || name == "ginstall":
		s.copyTargets(name, args)
	case name == "truncate":
		s.addWords(operands(args, map[string]bool{"-s": true, "-r": true, "--size": true, "--reference": true}))
	case name == "dd":
		for _, arg := range args[1:] {
			if value, ok := strings.CutPrefix(arg.text, "of="); ok {
				s.addWords([]word{{text: value, dynamic: arg.dynamic, glob: arg.glob}})
			}
		}
	case name == "patch":
		if !slices.ContainsFunc(args, func(w word) bool {
			return w.text == "--dry-run" || w.text == "--check" || w.text == "-C"
		}) {
			s.patchTargets(c, args)
		}
	case name == "git":
		s.gitTargets(c, args)
	case name == "apply_patch" || name == "applypatch":
		s.applyPatchTargets(c, args)
	case name == "hhx":
		s.declaration(args)
	case shells[name]:
		s.shellScript(c, args, depth)
	case name == "eval":
		if depth < maxNesting {
			s.walk(parseShell(joinWords(args[1:]), depth+1), depth+1)
		}
	}
}

func isFD(text string) bool {
	if text == "-" {
		return true
	}
	_, err := strconv.Atoi(text)
	return err == nil
}

// prefixKeywords はコマンドの前に置かれる予約語である。取り除いて実際のコマンド名を見る。
var prefixKeywords = map[string]bool{
	"!": true, "{": true, "then": true, "do": true, "else": true, "elif": true, "if": true, "while": true, "until": true,
}

// wrapperValued は、コマンドを引数に取るラッパーのうち、値を取るオプションである。
var wrapperValued = map[string]map[string]bool{
	"sudo":    {"-u": true, "-g": true, "-C": true, "-D": true, "-h": true, "-p": true, "-U": true},
	"env":     {"-u": true, "-C": true, "-S": true},
	"command": {},
	"exec":    {"-a": true},
	"nohup":   {},
	"nice":    {"-n": true},
	"time":    {},
	"rtk":     {},
	"timeout": {"-s": true, "-k": true, "--signal": true, "--kill-after": true},
}

var assignmentRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\+?=`)

// stripPrefixes は予約語・変数の代入・ラッパー（sudo・env・timeout など）を取り除いた語を返す。
func stripPrefixes(words []word) []word {
	for len(words) > 0 {
		first := words[0].text
		switch {
		case prefixKeywords[first]:
			words = words[1:]
		case assignmentRE.MatchString(first):
			words = words[1:]
		case wrapperValued[baseName(first)] != nil:
			valued := wrapperValued[baseName(first)]
			wrapper := baseName(first)
			words = words[1:]
			for len(words) > 0 {
				option := words[0].text
				if wrapper == "env" && assignmentRE.MatchString(option) {
					words = words[1:]
					continue
				}
				if !strings.HasPrefix(option, "-") || option == "-" {
					break
				}
				words = words[1:]
				if option == "--" {
					break
				}
				if valued[option] && len(words) > 0 {
					words = words[1:]
				}
			}
			if wrapper == "timeout" && len(words) > 0 {
				// 時間の引数を飛ばす。
				words = words[1:]
			}
		default:
			return words
		}
	}
	return words
}

func baseName(text string) string {
	return text[strings.LastIndex(text, "/")+1:]
}

func joinWords(words []word) string {
	texts := make([]string, len(words))
	for index, w := range words {
		texts[index] = w.text
	}
	return strings.Join(texts, " ")
}

// operands は args[1:] のうちオプションでない引数を返す。valued のオプションは次の引数を値として読み飛ばす。
// -- より後ろはすべて引数とする。
func operands(args []word, valued map[string]bool) []word {
	var out []word
	for index := 1; index < len(args); index++ {
		text := args[index].text
		switch {
		case text == "--":
			return append(out, args[index+1:]...)
		case strings.HasPrefix(text, "-") && text != "-":
			if valued[text] {
				index++
			}
		default:
			out = append(out, args[index])
		}
	}
	return out
}

func (s *bashScan) changeDirectory(args []word, push bool) {
	targets := operands(args, nil)
	previous := s.cwd
	switch {
	case len(targets) == 0:
		if home := py.Home(); home != "~" {
			s.cwd = home
		}
	case targets[0].text == "-":
		// cd - の行き先は追わない。
	default:
		// 行き先が決まらない・存在しないなら、作業ディレクトリは変えない（存在しない cd は実行時に失敗して留まる）。
		if resolved := s.resolve(targets[0]); len(resolved) > 0 && isDir(resolved[0]) {
			s.cwd = resolved[0]
		}
	}
	if push {
		s.dirs = append(s.dirs, previous)
	}
}
