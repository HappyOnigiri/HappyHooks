package generatededitguard

import (
	"io"
	"os"
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
	scan := &bashScan{cwd: cwd, seen: map[string]bool{}, direct: map[string]bool{}, copies: map[string][]string{},
		outputs: map[string]bool{}}
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

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// sedInPlace は sed の引数に -i（--in-place、-i.bak、-Ei のようにまとめた短いオプションを含む）があるかを返す。
func sedInPlace(args []word) bool {
	for _, arg := range args[1:] {
		text := arg.text
		if text == "--" {
			return false
		}
		if strings.HasPrefix(text, "--in-place") {
			return true
		}
		if !strings.HasPrefix(text, "-") || strings.HasPrefix(text, "--") {
			continue
		}
		for _, letter := range text[1:] {
			// -I は macOS の sed の in-place である。
			if letter == 'i' || letter == 'I' {
				return true
			}
			// -e・-f・-l は残りが値なので、その先の i は -i ではない。
			if letter == 'e' || letter == 'f' || letter == 'l' {
				break
			}
		}
	}
	return false
}

// inPlaceValued は perl・ruby の短いオプションのうち、残りか次の引数を値に取るものである。
var inPlaceValued = map[string]string{"perl": "eEMmIxdC0l", "ruby": "eErIC0xFlKTWU"}

// inPlace は perl・ruby の引数に -i（-pi・-i.bak のようにまとめたものを含む）があるかを返す。
// オプションは最初のオプションでない引数（スクリプトのファイルか、-e が無ければそのファイル）までで、-e の値はその間に挟まる。
func inPlace(args []word, valued string) bool {
	for index := 1; index < len(args); index++ {
		text := args[index].text
		if !strings.HasPrefix(text, "-") || text == "-" || text == "--" {
			return false
		}
		for position, letter := range text[1:] {
			if letter == 'i' {
				return true
			}
			if strings.ContainsRune(valued, letter) {
				// 値がくっついていなければ、次の引数が値である（-pe 'code'）。
				if position == len(text)-2 && (letter == 'e' || letter == 'E') {
					index++
				}
				break
			}
		}
	}
	return false
}

// scriptFiles は perl・ruby -i の対象のファイル（コードの後ろの引数）を返す。-e が無ければ最初の引数はスクリプトである。
func scriptFiles(args []word) []word {
	hasCode := false
	var files []word
	for index := 1; index < len(args); index++ {
		text := args[index].text
		switch {
		case text == "-e" || text == "-E":
			hasCode = true
			index++
		case strings.HasPrefix(text, "-") && text != "-":
			if strings.Contains(strings.TrimLeft(text, "-"), "e") {
				hasCode = true
				if strings.HasSuffix(text, "e") {
					index++
				}
			}
		default:
			files = append(files, args[index])
		}
	}
	if !hasCode && len(files) > 0 {
		files = files[1:]
	}
	return files
}

var awkNames = map[string]bool{"awk": true, "gawk": true}

// awkInPlace は gawk の -i inplace（--include=inplace）があるかを返す。
func awkInPlace(args []word) bool {
	for index := 1; index < len(args); index++ {
		text := args[index].text
		next := ""
		if index+1 < len(args) {
			next = args[index+1].text
		}
		if ((text == "-i" || text == "--include") && next == "inplace") || text == "-iinplace" ||
			text == "--include=inplace" {
			return true
		}
	}
	return false
}

// awkFiles は gawk -i inplace の対象のファイルを返す。-f / -e が無ければ最初の引数はプログラムである。
func awkFiles(args []word) []word {
	valued := map[string]bool{"-f": true, "-v": true, "-i": true, "-e": true, "-F": true, "--include": true,
		"--file": true, "--source": true, "--assign": true, "--field-separator": true}
	program := false
	for _, arg := range args[1:] {
		if arg.text == "-f" || arg.text == "-e" || arg.text == "--file" || arg.text == "--source" {
			program = true
		}
	}
	files := operands(args, valued)
	if !program && len(files) > 0 {
		files = files[1:]
	}
	var out []word
	for _, file := range files {
		if !strings.Contains(file.text, "=") {
			out = append(out, file)
		}
	}
	return out
}

// copyTargets は cp・mv・install の書き込み先を足す。最後の引数（-t があればその値）がディレクトリなら、その下の同名のファイルにする。
func (s *bashScan) copyTargets(name string, args []word) {
	valued := map[string]bool{"-t": true, "-S": true, "--target-directory": true, "--suffix": true}
	if name != "cp" && name != "mv" {
		valued = map[string]bool{"-t": true, "-S": true, "-m": true, "-o": true, "-g": true, "--mode": true,
			"--owner": true, "--group": true, "--target-directory": true, "--suffix": true}
	}
	var directory *word
	for index := 1; index < len(args); index++ {
		text := args[index].text
		if text == "-d" && name != "cp" && name != "mv" {
			// install -d はディレクトリを作るだけである。
			return
		}
		switch {
		case (text == "-t" || text == "--target-directory") && index+1 < len(args):
			directory = &args[index+1]
		case strings.HasPrefix(text, "--target-directory="):
			directory = &word{text: text[len("--target-directory="):], dynamic: args[index].dynamic}
		case strings.HasPrefix(text, "-t") && len(text) > 2:
			directory = &word{text: text[2:], dynamic: args[index].dynamic}
		}
	}
	files := operands(args, valued)
	if directory == nil {
		if len(files) < 2 {
			return
		}
		destination := files[len(files)-1]
		files = files[:len(files)-1]
		resolved := s.resolve(destination)
		if len(resolved) != 1 || !isDir(resolved[0]) {
			for _, path := range resolved {
				s.addCopy(path, s.sources(files))
			}
			return
		}
		directory = &destination
	}
	resolved := s.resolve(*directory)
	if len(resolved) != 1 {
		return
	}
	for _, file := range files {
		if !file.dynamic {
			s.addCopy(filepath.Join(resolved[0], filepath.Base(file.text)), s.sources([]word{file}))
		}
	}
}

// sources はコピーの送り元を解決する。静的に決まらない送り元があれば、空の送り元（生成ファイルとみなさない）を返す。
func (s *bashScan) sources(files []word) []string {
	var out []string
	for _, file := range files {
		resolved := s.resolve(file)
		if len(resolved) == 0 {
			return []string{""}
		}
		out = append(out, resolved...)
	}
	return out
}

// --- パッチ -------------------------------------------------------------------

var (
	// diffHeaderRE は unified diff の対象のファイルの行（--- と +++）である。タブより後ろ（日時）は含めない。
	diffHeaderRE = regexp.MustCompile(`(?m)^(?:\+\+\+|---)[ \t]+([^\t\r\n]+)`)
	// gitDiffRE は git の diff の見出し（diff --git a/<path> b/<path>）である。
	gitDiffRE = regexp.MustCompile(`(?m)^diff --git a/(\S+) b/(\S+)`)
	// patchFileRE は apply_patch の形式の対象のファイルの行である。
	patchFileRE = regexp.MustCompile(`(?m)^\*\*\* (?:Add File|Update File|Delete File|Move to): *([^\r\n]+?)[ \t]*\r?$`)
)

// diffPaths は diff のテキストに現れるパスを返す（/dev/null を除く）。
func diffPaths(text string) []string {
	var paths []string
	for _, match := range diffHeaderRE.FindAllStringSubmatch(text, -1) {
		path := strings.TrimSpace(match[1])
		if len(path) >= 2 && path[0] == '"' && path[len(path)-1] == '"' {
			if unquoted, err := strconv.Unquote(path); err == nil {
				path = unquoted
			}
		}
		if path != "/dev/null" && path != "" {
			paths = append(paths, path)
		}
	}
	for _, match := range gitDiffRE.FindAllStringSubmatch(text, -1) {
		paths = append(paths, "a/"+match[1], "b/"+match[2])
	}
	return paths
}

// stripComponents はパスの先頭から n 個の要素を除く。要素が足りなければ空文字列を返す。
func stripComponents(path string, n int) string {
	for range n {
		index := strings.IndexByte(path, '/')
		if index < 0 {
			return ""
		}
		path = strings.TrimLeft(path[index+1:], "/")
	}
	return path
}

// readSmall は通常のファイルを上限まで読む。読めなければ空文字列を返す。
func readSmall(path string) string {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	data, _ := io.ReadAll(io.LimitReader(file, maxPatchFileSize))
	return string(data)
}

// stdinText はコマンドの stdin に渡る静的なテキストを返す。ヒアドキュメント・here-string・パイプの前の echo / printf と、
// allowFiles なら < のファイルとパイプの前の cat のファイルも読む。
func (s *bashScan) stdinText(c *command, allowFiles bool) string {
	var texts []string
	for _, r := range c.redirects {
		switch r.op {
		case "<<", "<<-":
			texts = append(texts, r.body)
		case "<<<":
			texts = append(texts, r.target.text)
		case "<":
			if allowFiles {
				for _, path := range s.resolve(r.target) {
					texts = append(texts, readSmall(path))
				}
			}
		}
	}
	if source := c.pipedFrom; source != nil {
		for _, r := range source.redirects {
			switch r.op {
			case "<<", "<<-":
				texts = append(texts, r.body)
			case "<<<":
				texts = append(texts, r.target.text)
			}
		}
		args := stripPrefixes(source.words)
		if len(args) > 0 {
			switch baseName(args[0].text) {
			case "echo", "printf":
				texts = append(texts, joinWords(args[1:]))
			case "cat":
				if allowFiles {
					for _, file := range operands(args, nil) {
						for _, path := range s.resolve(file) {
							texts = append(texts, readSmall(path))
						}
					}
				}
			}
		}
	}
	return strings.Join(texts, "\n")
}

// patchTargets は patch の書き込み先を足す。
// 引数に元のファイルがあればそれを、-o があればその出力を、どちらも無ければパッチの中のパスを候補にする。
func (s *bashScan) patchTargets(c *command, args []word) {
	valued := map[string]bool{"-p": true, "-d": true, "-i": true, "-o": true, "-B": true, "-D": true, "-F": true,
		"-g": true, "-r": true, "-V": true, "-Y": true, "-z": true, "--strip": true, "--directory": true,
		"--input": true, "--output": true}
	strip := -1
	directory := s.cwd
	var inputs, outputs []word
	for index := 1; index < len(args); index++ {
		text := args[index].text
		next := word{}
		if index+1 < len(args) {
			next = args[index+1]
		}
		switch {
		case text == "-p" || text == "--strip":
			strip, _ = strconv.Atoi(next.text)
		case strings.HasPrefix(text, "-p"):
			strip, _ = strconv.Atoi(text[2:])
		case strings.HasPrefix(text, "--strip="):
			strip, _ = strconv.Atoi(text[len("--strip="):])
		case text == "-d" || text == "--directory":
			if resolved := s.resolve(next); len(resolved) == 1 {
				directory = resolved[0]
			}
		case text == "-i" || text == "--input":
			inputs = append(inputs, next)
		case text == "-o" || text == "--output":
			outputs = append(outputs, next)
		}
	}
	if len(outputs) > 0 {
		s.addWords(outputs)
		return
	}
	files := operands(args, valued)
	if len(files) > 0 {
		s.addWords(files[:1])
		if len(files) > 1 {
			inputs = append(inputs, files[1])
		}
	}
	text := s.stdinText(c, true)
	for _, input := range inputs {
		for _, path := range s.resolve(input) {
			text += "\n" + readSmall(path)
		}
	}
	for _, path := range diffPaths(text) {
		candidates := []string{path, stripComponents(path, 1), filepath.Base(path)}
		if strip >= 0 {
			candidates = []string{stripComponents(path, strip)}
		}
		for _, candidate := range candidates {
			if candidate != "" {
				s.add(joinUnder(directory, candidate))
			}
		}
	}
}

// joinUnder は path を directory から解決する（絶対パスならそのまま）。
func joinUnder(directory, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(directory, path)
}

// gitTargets は git の大域オプション（-C を含む）を読み、git apply の書き込み先を足す。
func (s *bashScan) gitTargets(c *command, args []word) {
	directory := s.cwd
	index := 1
	for ; index < len(args); index++ {
		text := args[index].text
		switch {
		case text == "-C" && index+1 < len(args):
			index++
			resolved := s.resolveFrom(directory, args[index])
			if resolved == "" {
				return
			}
			directory = resolved
		case text == "-c" || text == "--git-dir" || text == "--work-tree" || text == "--namespace":
			index++
		case strings.HasPrefix(text, "-"):
		default:
			if text == "apply" {
				s.gitApplyTargets(c, args[index:], directory)
			}
			return
		}
	}
}

// resolveFrom は語を directory から解決する。静的に決まらなければ空文字列を返す。
func (s *bashScan) resolveFrom(directory string, w word) string {
	saved := s.cwd
	s.cwd = directory
	resolved := s.resolve(w)
	s.cwd = saved
	if len(resolved) != 1 {
		return ""
	}
	return resolved[0]
}

// gitApplyTargets は git apply のパッチの中のパスを、作業ツリーの根と作業ディレクトリの両方から解決して足す。
// 作業ツリーの中では git apply のパスは根からの相対だが、外では作業ディレクトリからの相対になるため両方を候補にする。
func (s *bashScan) gitApplyTargets(c *command, args []word, directory string) {
	strip := 1
	root := ""
	onlyIndex, report, apply := false, false, false
	var files []word
	for index := 1; index < len(args); index++ {
		text := args[index].text
		switch {
		case text == "--check" || text == "--stat" || text == "--numstat" || text == "--summary":
			report = true
		case text == "--apply":
			apply = true
		case text == "--cached":
			onlyIndex = true
		case strings.HasPrefix(text, "-p") && len(text) > 2:
			strip, _ = strconv.Atoi(text[2:])
		case text == "-p" && index+1 < len(args):
			index++
			strip, _ = strconv.Atoi(args[index].text)
		case strings.HasPrefix(text, "--directory="):
			root = text[len("--directory="):]
		case text == "-" || !strings.HasPrefix(text, "-"):
			files = append(files, args[index])
		}
	}
	if (report && !apply) || onlyIndex {
		return
	}
	text := ""
	if len(files) == 0 || slices.ContainsFunc(files, func(w word) bool { return w.text == "-" }) {
		text = s.stdinText(c, true)
	}
	for _, file := range files {
		if path := s.resolveFrom(directory, file); path != "" {
			text += "\n" + readSmall(path)
		}
	}
	top := worktreeTop(directory)
	for _, path := range diffPaths(text) {
		stripped := stripComponents(path, strip)
		if stripped == "" {
			continue
		}
		if root != "" {
			stripped = filepath.Join(root, stripped)
		}
		s.add(joinUnder(directory, stripped))
		if top != "" {
			s.add(joinUnder(top, stripped))
		}
	}
}

// worktreeTop は directory から親へ .git を探し、作業ツリーの根を返す。見つからなければ空文字列を返す。
// git を起動しないのは、全 Bash 呼び出しで走る hook を軽く保つためである。
func worktreeTop(directory string) string {
	for current := directory; ; {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

// applyPatchTargets はシェルから起動した apply_patch（Codex の形式）のパッチの中のパスを足す。
func (s *bashScan) applyPatchTargets(c *command, args []word) {
	text := s.stdinText(c, false) + "\n" + joinWords(args[1:])
	for _, match := range patchFileRE.FindAllStringSubmatch(text, -1) {
		s.add(s.absolute(match[1]))
	}
}

// --- その場で渡したコード ---------------------------------------------------------

var (
	pythonRE = regexp.MustCompile(`^(?:python|pypy)[0-9.]*$`)
	rubyRE   = regexp.MustCompile(`^ruby[0-9.]*$`)
	perlRE   = regexp.MustCompile(`^perl[0-9.]*$`)
	shells   = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true}
)

// interpreterKind はコマンド名からインタプリタの種類を返す。インタプリタでなければ空文字列を返す。
func interpreterKind(name string) string {
	switch {
	case pythonRE.MatchString(name):
		return "python"
	case name == "node" || name == "nodejs":
		return "node"
	case rubyRE.MatchString(name):
		return "ruby"
	case perlRE.MatchString(name):
		return "perl"
	}
	return ""
}

// interpreterOptions はインタプリタごとの、コードを値に取るオプションと、それ以外の値を取るオプションである。
var interpreterOptions = map[string]struct{ code, valued map[string]bool }{
	"python": {code: map[string]bool{"-c": true}, valued: map[string]bool{"-W": true, "-X": true}},
	"node": {
		code:   map[string]bool{"-e": true, "--eval": true, "-p": true, "--print": true},
		valued: map[string]bool{"-r": true, "--require": true, "--import": true, "--loader": true},
	},
	"ruby": {code: map[string]bool{"-e": true}, valued: map[string]bool{"-r": true, "-I": true, "-C": true, "-E": true}},
	"perl": {code: map[string]bool{"-e": true, "-E": true}, valued: map[string]bool{}},
}

// inlineCode はインタプリタにその場で渡したコードを読み、書き込みの API があれば、コードの中のパスに見える文字列を候補にする。
// スクリプトのファイルやモジュールを実行する形（python3 tools/x.py、python3 -m x）は外部のスクリプトなので見ず、偽を返す。
func (s *bashScan) inlineCode(kind string, c *command, args []word) bool {
	options := interpreterOptions[kind]
	var code []string
	stdin := false
	for index := 1; index < len(args); index++ {
		text := args[index].text
		if options.code[text] || pythonCodeCluster(kind, text) {
			if index+1 < len(args) {
				code = append(code, args[index+1].text)
			}
			index++
			continue
		}
		if value, ok := attachedCode(kind, text); ok {
			code = append(code, value)
			continue
		}
		switch {
		case text == "-":
			stdin = true
		case kind == "python" && text == "-m":
			return false
		case options.valued[text]:
			index++
		case strings.HasPrefix(text, "-") && text != "--":
		default:
			// 最初の引数はスクリプトのファイルである。コードを渡した後なら、スクリプトへの引数である。
			if len(code) == 0 {
				return false
			}
			index = len(args)
		}
	}
	if len(code) == 0 && (stdin || len(operands(args, options.valued)) == 0) {
		code = append(code, s.stdinText(c, false))
	}
	s.codeTargets(strings.Join(code, "\n"))
	return true
}

// pythonCodeCluster は -c で終わる短いオプションのまとまり（-Bc）で、次の引数がコードになる形かを返す。
func pythonCodeCluster(kind, text string) bool {
	return kind == "python" && len(text) > 2 && strings.HasPrefix(text, "-") && !strings.HasPrefix(text, "--") &&
		strings.HasSuffix(text, "c") && strings.Trim(text[1:len(text)-1], "bBdEhiIOPqsSuv") == ""
}

// attachedCode は -c'...'・--eval=... のように値をくっつけたコードのオプションを読む。
func attachedCode(kind, text string) (string, bool) {
	switch kind {
	case "python":
		// -c の前に値を取らない短いオプションをまとめた形（-Bc）も読む。
		if !strings.HasPrefix(text, "-") || strings.HasPrefix(text, "--") {
			break
		}
		index := strings.IndexByte(text, 'c')
		if index > 0 && index < len(text)-1 && strings.Trim(text[1:index], "bBdEhiIOPqsSuv") == "" {
			return text[index+1:], true
		}
	case "node":
		for _, prefix := range []string{"--eval=", "--print="} {
			if value, ok := strings.CutPrefix(text, prefix); ok {
				return value, true
			}
		}
	case "ruby", "perl":
		if (strings.HasPrefix(text, "-e") || (kind == "perl" && strings.HasPrefix(text, "-E"))) && len(text) > 2 {
			return text[2:], true
		}
	}
	return "", false
}

var (
	// writeAPIRE はコードの中の書き込みの API である。書き込みモードの open（Python・Ruby・Node の 'w'・'a'・'x'・'+' と、
	// Perl の '>'・'>>'・'+<'）と、ファイルへ書き込む関数を拾う。open の括弧の中は 1 段の入れ子まで見る。
	writeAPIRE = regexp.MustCompile(`open\w*\s*\((?:[^()]|\([^()]*\))*?` +
		`(?:['"][rbtU]*[wax+][rbtU+]*['"]|['"](?:>>?|\+[<>]))` +
		`|\bopen\s*\(?\s*(?:my\s+)?[$\w]+\s*,\s*['"](?:>>?|\+[<>])` +
		`|write_text|write_bytes|\.write\s*\(|writeFile|appendFile|createWriteStream|renameSync|File\.write` +
		`|shutil\.(?:copy\w*|move)|copyFile|os\.(?:rename|replace)`)
	// literalRE はコードの中の文字列リテラル（' " ` で囲んだもの）である。
	literalRE = regexp.MustCompile("'([^'\\\\\n]*)'|\"([^\"\\\\\n]*)\"|`([^`\\\\\n$]*)`")
	// pathLikeRE はパスに見える文字列（空白を含まず、/ か拡張子の . を含む）である。
	pathLikeRE = regexp.MustCompile(`^[^\s]*(?:/|\.[A-Za-z0-9_]+$)[^\s]*$`)
)

// codeTargets はコードに書き込みの API があれば、コードの中のパスに見える文字列リテラルを候補にする。
func (s *bashScan) codeTargets(code string) {
	if !writeAPIRE.MatchString(code) {
		return
	}
	for _, match := range literalRE.FindAllStringSubmatch(code, -1) {
		for _, literal := range match[1:] {
			// Perl の 2 引数の open（">gen.go"）は、モードの記号を除いたものをパスとみなす。
			literal = strings.TrimLeft(literal, "+<> ")
			if literal == "" || len(literal) > 1024 || strings.Contains(literal, "://") || !pathLikeRE.MatchString(literal) {
				continue
			}
			s.add(s.absolute(literal))
		}
	}
}

// shellScript は sh -c '<script>' と、ヒアドキュメントで渡したスクリプトを入れ子のコマンドとして走査する。
func (s *bashScan) shellScript(c *command, args []word, depth int) {
	if depth >= maxNesting {
		return
	}
	script := ""
	for index := 1; index < len(args); index++ {
		text := args[index].text
		if text == "-o" || text == "-O" || text == "+o" || text == "+O" {
			// set -o と shopt の値（pipefail・extglob）を読み飛ばす。
			index++
			continue
		}
		if strings.HasPrefix(text, "-") && !strings.HasPrefix(text, "--") && strings.Contains(text, "c") {
			if index+1 < len(args) {
				script = args[index+1].text
			}
			break
		}
		if !strings.HasPrefix(text, "-") {
			// スクリプトのファイルを実行する形は外部のスクリプトなので見ない。
			return
		}
	}
	if script == "" {
		script = s.stdinText(c, false)
	}
	if script == "" {
		return
	}
	s.outer = append(s.outer, s.cwd)
	s.walk(parseShell(script, depth+1), depth+1)
	s.cwd = s.outer[len(s.outer)-1]
	s.outer = s.outer[:len(s.outer)-1]
}

// declaration はコマンドが宣言（hhx allow-generated-edit ...）なら、パスを解決して記録する。
// 形の誤った宣言は記録しない（CLI が使い方を示して失敗する）。
func (s *bashScan) declaration(args []word) {
	if len(args) < 2 || args[1].text != DeclareCommand {
		return
	}
	texts := make([]string, 0, len(args)-2)
	for _, arg := range args[2:] {
		texts = append(texts, arg.text)
	}
	reason, indexes, err := parseDeclaration(texts)
	if err != nil {
		return
	}
	var paths, unresolved []string
	for _, index := range indexes {
		arg := args[2+index]
		if arg.dynamic {
			unresolved = append(unresolved, arg.text)
			continue
		}
		for _, path := range s.resolve(arg) {
			if !slices.Contains(paths, path) {
				paths = append(paths, path)
			}
		}
	}
	if len(paths) > 0 || len(unresolved) > 0 {
		s.declarations = append(s.declarations, declared{reason: reason, paths: paths, unresolved: unresolved})
	}
}
