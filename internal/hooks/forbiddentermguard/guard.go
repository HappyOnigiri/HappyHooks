// Package forbiddentermguard は、PR や issue の本文に禁止語が入るのを PreToolUse で拒否する hook（forbidden-term-guard）である。
//
// 方針:
//   - 有効にするのはリポジトリ単位のオプトインである。共通の git ディレクトリの直下に forbidden-terms.txt がある
//     リポジトリでだけ検査する。語そのものをどのリポジトリにもコミットさせないため、語リストは Git の管理外に置く。
//     書式は docs/forbidden-terms.md にある。
//   - 対象は本文をリモートへ送るコマンド（gh pr / gh issue の create・edit・comment と gh api）である。
//     git commit と git push は git の commit-msg / pre-push hook が受け持つので対象外にする。
//     対象がすべて読み取りの gh api なら検査しない（onlyReadOnlyAPI）。
//   - コマンド文字列に加えて、--body-file / -F で渡すファイルの中身も読む。
//   - 変数展開・別シェル経由といった明示的な回避は対象外（README の既知の限界）。
//
// 語リストが無いリポジトリでは、ファイルの存在の確認だけで抜ける。git を含め外部コマンドは起動しない。
package forbiddentermguard

import (
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/HappyOnigiri/hhx/internal/hookrt"
	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

// Name は hook の名前である。
const Name = "forbidden-term-guard"

const (
	// termsFileName は語リストのファイル名である。共通の git ディレクトリの直下に置く。
	termsFileName = "forbidden-terms.txt"
	maxReported   = 10
	maxLine       = 200
	maxBodyBytes  = 1 << 20
)

// Definition は forbidden-term-guard の定義を返す。
func Definition() hookrt.Definition {
	return hookrt.Definition{
		Name:           Name,
		DefaultEnabled: true,
		Registrations: []hookrt.Registration{
			{Agent: hookrt.Claude, Event: "PreToolUse", Matcher: "Bash"},
			{Agent: hookrt.Codex, Event: "PreToolUse", Matcher: "Bash"},
		},
		// 検査の対象はすべて gh のサブコマンドである。
		Gate: func(input []byte) bool { return strings.Contains(string(input), "gh") },
		Run:  run,
	}
}

var (
	// 本文をリモートへ送るコマンド。gh api はエンドポイントを問わず対象にする（本文を含む書き込みが REST でも通るため）。
	targetRE = regexp.MustCompile(`(?:^|[;&|(]|` + py.Space + `)(?:` + py.NotSpace + `*/)?gh` + py.Space +
		`+(?:(?:pr|issue)` + py.Space + `+(?:create|edit|comment)|api)` + py.NotWordOrEnd)
	// --body-file <path> / -F <path> / --body-file=<path>
	bodyFileRE = regexp.MustCompile(`(?:--body-file|-F)[=` + py.SpaceChars + `]+([^;&|` + py.SpaceChars + `]+)`)
)

func run(c *hookrt.Context) error {
	command, cwd, ok := commandAndCWD(c)
	if !ok || command == "" || !targetRE.MatchString(command) || onlyReadOnlyAPI(command) {
		return nil
	}
	gitDir := commonDir(cwd)
	if gitDir == "" {
		return nil
	}
	termsPath := joinPath(gitDir, termsFileName)
	if !isFile(termsPath) {
		// このリポジトリは対象外である。
		return nil
	}
	terms, invalid := loadTerms(termsPath)
	if len(invalid) > 0 {
		// 検査できない語があるまま送ると、その語が公開先へ出てしまう。
		c.Deny(invalidReason(c.Language(), invalid, termsPath))
		return nil
	}
	if len(terms) == 0 {
		return nil
	}
	findings := scan(command, terms, "command", nil)
	for _, file := range bodyFiles(command, cwd) {
		info, err := os.Stat(file.path)
		if err != nil || info.Size() > maxBodyBytes {
			continue
		}
		data, err := os.ReadFile(file.path)
		if err != nil {
			continue
		}
		findings = scan(strings.ToValidUTF8(string(data), "�"), terms, file.shown, findings)
	}
	if len(findings) > 0 {
		c.Deny(reason(c.Language(), findings, termsPath))
	}
	return nil
}

// 移植元との違い: 移植元は gh api をメソッドを問わず対象にしていた。読み取りの gh api（別リポジトリの情報を引く
// graphql の query など）は本文を公開先へ書き込まないのに、語がリポジトリ名や org 名だと止まっていたので、
// コマンドの中の対象がすべて読み取りの gh api と確かめられたときだけ検査しない。
// 確かめられないとき（$(...)・バッククォート・閉じない引用・gh を別のコマンド越しに起動する形など）は従来どおり検査する。

// apiSeparators は gh の呼び出しを区切る字句である。
const apiSeparators = ";&|()\n"

// apiValueFlags は値を 1 つ取る gh api のフラグである（gh 2.x の gh api --help）。
var apiValueFlags = map[string]bool{
	"-X": true, "--method": true, "-F": true, "--field": true, "-f": true, "--raw-field": true,
	"--input": true, "-H": true, "--header": true, "-q": true, "--jq": true, "-t": true, "--template": true,
	"--hostname": true, "--cache": true, "-p": true, "--preview": true,
}

// apiBoolFlags は値を取らない gh api のフラグである。
var apiBoolFlags = map[string]bool{
	"--paginate": true, "--slurp": true, "-i": true, "--include": true, "--silent": true, "--verbose": true,
	"--allow-escape-sequences": true,
}

// onlyReadOnlyAPI は、command の中の検査の対象（targetRE に一致する箇所）がすべて読み取りの gh api かを返す。
// 字句に区切って見つけた gh の呼び出しの数が targetRE の一致の数と違えば、見えない呼び出しがあるので偽を返す。
func onlyReadOnlyAPI(command string) bool {
	if strings.Contains(command, "$(") || strings.Contains(command, "`") {
		return false
	}
	tokens, err := py.ShlexSplit(command, " \t\r", apiSeparators)
	if err != nil {
		return false
	}
	found := 0
	start := true
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if strings.Trim(token, apiSeparators) == "" {
			start = true
			continue
		}
		if !start {
			continue
		}
		start = false
		if token != "gh" && !strings.HasSuffix(token, "/gh") {
			continue
		}
		end := index + 1
		for end < len(tokens) && strings.Trim(tokens[end], apiSeparators) != "" {
			end++
		}
		args := tokens[index+1 : end]
		if len(args) == 0 {
			continue
		}
		switch args[0] {
		case "api":
			found++
			if !readOnlyAPI(args[1:]) {
				return false
			}
		case "pr", "issue":
			// 作成・編集・コメントでなければ対象ではない（targetRE と同じ条件）。
			if len(args) > 1 && (args[1] == "create" || args[1] == "edit" || args[1] == "comment") {
				return false
			}
		}
		index = end - 1
	}
	return found > 0 && found == len(targetRE.FindAllStringIndex(command, -1))
}

// readOnlyAPI は gh api の引数（api より後）が読み取りの要求かを返す。
//   - -X / --method があれば、GET のときだけ読み取りとする（フィールドはクエリ文字列になる）。
//   - 無ければ、--input もフィールド（-f / -F）も無いときは GET なので読み取りとする。
//   - エンドポイントが graphql なら、フィールドの値に mutation を含まず、ファイルから読む値（@path）も無いときだけ読み取りとする。
//
// 知らないフラグがあれば、値を取るかが分からずエンドポイントを特定できないので偽を返す。
func readOnlyAPI(args []string) bool {
	method := ""
	endpoint := ""
	hasInput := false
	var fields []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		name, value, joined := splitFlag(arg)
		switch {
		case name == "":
			if endpoint == "" {
				endpoint = arg
			}
			continue
		case apiBoolFlags[name]:
			if joined {
				return false
			}
			continue
		case !apiValueFlags[name]:
			return false
		}
		if !joined {
			if index+1 >= len(args) {
				return false
			}
			index++
			value = args[index]
		}
		switch name {
		case "-X", "--method":
			method = value
		case "--input":
			hasInput = true
		case "-f", "--raw-field", "-F", "--field":
			fields = append(fields, value)
		}
	}
	if method != "" {
		return strings.EqualFold(method, "GET")
	}
	if hasInput {
		return false
	}
	if strings.TrimPrefix(endpoint, "/") != "graphql" {
		return len(fields) == 0
	}
	for _, field := range fields {
		_, value, _ := strings.Cut(field, "=")
		if strings.HasPrefix(value, "@") || strings.Contains(strings.ToLower(value), "mutation") {
			return false
		}
	}
	return true
}

// splitFlag は arg をフラグの名前と、名前に続けて書いた値（--method=GET・-XGET・-fquery=...）に分ける。
// フラグでなければ name は空である。
func splitFlag(arg string) (name, value string, joined bool) {
	switch {
	case !strings.HasPrefix(arg, "-") || arg == "-" || arg == "--":
		return "", "", false
	case strings.HasPrefix(arg, "--"):
		name, value, joined = strings.Cut(arg, "=")
		return name, value, joined
	case len(arg) > 2:
		return arg[:2], strings.TrimPrefix(arg[2:], "="), true
	default:
		return arg, "", false
	}
}

// commandAndCWD は判定するコマンドと、その実行ディレクトリを返す。
// payload が JSON の object でないか、tool_input が object でなければ（移植元で AttributeError になっていた形）、
// argv の経路ではその文字列をコマンドとし、プロセスの cwd を使う。stdin の経路では何もしない（fail-open）。
func commandAndCWD(c *hookrt.Context) (command, cwd string, ok bool) {
	var payload any
	if json.Unmarshal(c.Input, &payload) == nil {
		if object, isObject := payload.(map[string]any); isObject {
			if toolInput, present := object["tool_input"]; !present {
				return fromPayload(object, map[string]any{})
			} else if mapping, isMapping := toolInput.(map[string]any); isMapping {
				return fromPayload(object, mapping)
			}
		}
	}
	if !c.FromArgs {
		return "", "", false
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", false
	}
	return string(c.Input), cwd, true
}

// fromPayload は payload の object と tool_input から command と cwd を取り出す。
// 移植元で例外になって無出力で終わっていた形（command が文字列でない、cwd が空でない文字列以外）は、何もしない。
func fromPayload(object, toolInput map[string]any) (command, cwd string, ok bool) {
	if value, present := toolInput["command"]; present {
		if command, ok = value.(string); !ok {
			return "", "", false
		}
	}
	switch value := object["cwd"].(type) {
	case string:
		cwd = value
	default:
		if truthy(value) {
			return "", "", false
		}
	}
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return "", "", false
		}
	}
	return command, cwd, true
}

// truthy は JSON の値が Python で真とみなされるかを返す。
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case float64:
		return typed != 0
	case string:
		return typed != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

// commonDir は cwd から共通の git ディレクトリを求める。git は起動しない。見つからなければ空文字列を返す。
// linked worktree では、.git ファイルの gitdir: と、その先の commondir を解決する。
func commonDir(start string) string {
	if start == "" {
		return ""
	}
	current, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	gitDir := ""
	for gitDir == "" {
		candidate := filepath.Join(current, ".git")
		info, err := os.Stat(candidate)
		switch {
		case err == nil && info.IsDir():
			gitDir = candidate
		case err == nil && info.Mode().IsRegular():
			// linked worktree の .git は `gitdir: <path>` の 1 行のファイルである。
			data, err := os.ReadFile(candidate)
			if err != nil {
				return ""
			}
			head := py.Strip(firstLine(strings.ToValidUTF8(string(data), "�")))
			rest, found := strings.CutPrefix(head, "gitdir:")
			if !found {
				return ""
			}
			gitDir = joinPath(current, py.Strip(rest))
		default:
			parent := filepath.Dir(current)
			if parent == current {
				return ""
			}
			current = parent
		}
	}
	// linked worktree の git ディレクトリは、commondir で共通の側を指す。
	common := joinPath(gitDir, "commondir")
	if !isFile(common) {
		return gitDir
	}
	data, err := os.ReadFile(common)
	if err != nil {
		return gitDir
	}
	if relative := py.Strip(strings.ToValidUTF8(string(data), "�")); relative != "" {
		return joinPath(gitDir, relative)
	}
	return gitDir
}

// firstLine は Python のテキストモードの readline() と同じく、最初の改行（\n・\r\n・\r）までを返す。
func firstLine(text string) string {
	if index := strings.IndexAny(text, "\r\n"); index >= 0 {
		return text[:index]
	}
	return text
}

// joinPath は Python の os.path.normpath(os.path.join(base, name)) と同じく、name が絶対パスならそれを使う。
func joinPath(base, name string) string {
	if filepath.IsAbs(name) {
		return filepath.Clean(name)
	}
	return filepath.Join(base, name)
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// termPattern は語リストの 1 行を、大文字小文字を区別しない部分一致の正規表現にしたものである。
type termPattern = *regexp.Regexp

// loadTerms は語リストを読む。書式は docs/forbidden-terms.md にある。読めなければ空を返す。
// invalid はコンパイルできない re: の行の行番号である。
func loadTerms(path string) (terms []termPattern, invalid []int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	return parseTerms(string(data))
}

// newlineRE は Python のテキストモード（universal newlines）が行の区切りとみなす改行である。
var newlineRE = regexp.MustCompile(`\r\n|\r|\n`)

// parseTerms は語リストの中身を解釈する。
// RE2 でコンパイルできない re: の行（Python の re では書ける後読みなど）は、1 始まりの行番号を invalid に返す。
func parseTerms(text string) (terms []termPattern, invalid []int) {
	for index, raw := range newlineRE.Split(strings.ToValidUTF8(text, "�"), -1) {
		line := py.Strip(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if expression, isRegexp := strings.CutPrefix(line, "re:"); isRegexp {
			expression = py.Strip(expression)
			if expression == "" {
				continue
			}
			pattern, err := regexp.Compile("(?i)" + expression)
			if err != nil {
				invalid = append(invalid, index+1)
				continue
			}
			terms = append(terms, pattern)
			continue
		}
		terms = append(terms, regexp.MustCompile("(?i)"+literalPattern(line)))
	}
	return terms, invalid
}

// dottedI は、Python の re.IGNORECASE が互いに一致させ、Go の (?i) は i・I と İ・ı を区別する 4 文字である。
// Python は 1 文字ずつの小文字化（İ→i）と、同じ大文字を持つ小文字の表（i と ı）で照合する。
// ほかの文字の大文字小文字の対応は、Go の (?i) と一致する（Unicode のバージョンで増えた文字を除く）。
const dottedI = "iIİı"

// literalPattern は、語を Python の re.escape と re.IGNORECASE の組と同じ文字に一致する正規表現にする。
func literalPattern(term string) string {
	var pattern strings.Builder
	for _, r := range term {
		if strings.ContainsRune(dottedI, r) {
			pattern.WriteString("[" + dottedI + "]")
			continue
		}
		pattern.WriteString(regexp.QuoteMeta(string(r)))
	}
	return pattern.String()
}

// matchesAny は text が語のどれかを含むかを返す。
func matchesAny(text string, terms []termPattern) bool {
	for _, pattern := range terms {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

// scan は text の行ごとに語を探し、該当した行を findings に足して返す。
func scan(text string, terms []termPattern, location string, findings []string) []string {
	for index, line := range py.SplitLines(text) {
		if matchesAny(line, terms) {
			findings = append(findings, "  "+location+":"+strconv.Itoa(index+1)+": "+clip(line))
		}
	}
	return findings
}

// clip は該当した行を 1 行 maxLine 文字（コードポイント）で切る。
func clip(text string) string {
	runes := []rune(py.Strip(text))
	if len(runes) > maxLine {
		return string(runes[:maxLine]) + " …"
	}
	return string(runes)
}

type bodyFile struct {
	// shown は理由文に出す表記（コマンドに書かれたまま）、path は読むファイルのパスである。
	shown string
	path  string
}

// bodyFiles は --body-file / -F で渡すファイルを返す。- （stdin）は読まない。
func bodyFiles(command, cwd string) []bodyFile {
	var files []bodyFile
	for _, match := range bodyFileRE.FindAllStringSubmatch(command, -1) {
		raw := strings.Trim(match[1], `'"`)
		if raw == "" || raw == "-" {
			continue
		}
		path := raw
		if !filepath.IsAbs(raw) {
			base := cwd
			if base == "" {
				base = "."
			}
			path = pythonJoin(base, raw)
		}
		files = append(files, bodyFile{shown: raw, path: expandUser(path)})
	}
	return files
}

// pythonJoin は Python の os.path.join と同じく、区切りを足すだけで正規化しない（相対の .. を残す）。
func pythonJoin(base, name string) string {
	if strings.HasSuffix(base, "/") {
		return base + name
	}
	return base + "/" + name
}

// expandUser は Python の os.path.expanduser と同じく、先頭の ~ と ~user をホームディレクトリに置き換える。
func expandUser(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	name, rest, _ := strings.Cut(path[1:], "/")
	var home string
	if name == "" {
		home = os.Getenv("HOME")
		if home == "" {
			if current, err := user.Current(); err == nil {
				home = current.HomeDir
			}
		}
	} else if account, err := user.Lookup(name); err == nil {
		home = account.HomeDir
	}
	if home == "" {
		return path
	}
	if !strings.Contains(path, "/") {
		return home
	}
	return strings.TrimRight(home, "/") + "/" + rest
}
