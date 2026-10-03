// Package prbodystaleness は、push の成功後に PR 本文が差分より古くなっていたら注意をコンテキストへ注入する hook
// （pr-body-staleness）である。
//
// 狙いは 1 つだけで、commit を積んで push した結果、PR 本文が実装と食い違ったまま残るのを避ける。
// 判定: PR 本文の最終更新時刻（lastEditedAt、無ければ createdAt）より後の commit が PR に載っていれば「本文が古い可能性」とみなす。
// merge commit は base の取り込みで PR の説明を変えないので数えない。
//
// PostToolUse に置く理由と、発火しない条件（dry-run・ブランチの削除・Everything up-to-date・失敗した push・GitHub 以外）は
// push-ci-context と同じ。加えてこの hook に固有の条件:
//   - `gh pr create` では発火しない（作りたての本文は差分と同時に書かれている）
//   - push した HEAD に紐づく open の PR が無ければ発火しない
//   - gh が失敗・遅延した場合は無出力で終える（push を邪魔しない）
//
// 既知の限界: rebase / amend / cherry-pick は committedDate が振り直されるので、本文が正しくても発火することがある。
// 止めはせず注意に留めるので、実害は「1 回確認する」だけに収まる。逆に、古い commit を後から取り込んだ場合は取りこぼす。
//
// push の判定は push-ci-context と別の実装で、意味も違う（こちらは正規表現で区切り、引用符などを消してから空白で分ける）。
// デバッグ経路（`hhx hook pr-body-staleness '<コマンド文字列>'`）の引数はコマンド文字列で、プロセスの作業ディレクトリの
// リポジトリと HEAD を使う。
package prbodystaleness

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/HappyOnigiri/hhx/internal/hookexec"
	"github.com/HappyOnigiri/hhx/internal/hookrt"
	"github.com/HappyOnigiri/hhx/internal/i18n"
	py "github.com/HappyOnigiri/hhx/internal/pycompat"
	"github.com/HappyOnigiri/hhx/internal/toolresponse"
)

// Name は hook の名前である。
const Name = "pr-body-staleness"

// statusMessage は実行中に CLI が表示する文言である。
const statusMessage = "Checking PR body freshness..."

// Definition は pr-body-staleness の定義を返す。
func Definition() hookrt.Definition {
	return hookrt.Definition{
		Name:           Name,
		DefaultEnabled: true,
		Registrations: []hookrt.Registration{
			{Agent: hookrt.Claude, Event: "PostToolUse", Matcher: "Bash", Timeout: 15, StatusMessage: statusMessage},
			{Agent: hookrt.Codex, Event: "PostToolUse", Matcher: "Bash", Timeout: 15, StatusMessage: statusMessage,
				AdditionalContextLimit: 4096},
		},
		Gate:        gate,
		Run:         run,
		NewSettings: func() any { return &Settings{} },
	}
}

// Settings は pr-body-staleness 固有の設定である。
type Settings struct {
	Enabled *bool `yaml:"enabled"`
	// UpdateInstruction は、本文とコミットが食い違っていたときの本文の更新方法の案内（1 文）である。
	// 空なら既定の文面（update-pr スキルを使う案内）を使う。文末の記号が無ければ句点を足す。
	UpdateInstruction string `yaml:"update-instruction"`
}

const (
	// commandTimeout は git と gh の 1 回の上限である。
	commandTimeout = 8 * time.Second
	// maxHeadlines は案内に載せる commit の件数である。
	maxHeadlines = 5
	// commitWindow は本文より後の commit を探す範囲である。これを超えて積まれた PR は先頭の数件で十分伝わる。
	commitWindow = 50
)

var query = fmt.Sprintf(`
query($owner:String!,$repo:String!,$sha:GitObjectID!){
  repository(owner:$owner,name:$repo){
    object(oid:$sha){
      ... on Commit {
        associatedPullRequests(first:3){
          nodes{
            number url state lastEditedAt createdAt
            commits(last:%d){nodes{commit{committedDate messageHeadline parents{totalCount}}}}
          }
        }
      }
    }
  }
}
`, commitWindow)

// gate は push を含まない入力を抜ける（大半はここで終わる）。
func gate(input []byte) bool {
	return strings.Contains(py.Lower(string(input)), "push")
}

// errPythonException は、移植元では例外になって無出力で終わっていた入力を表す。hookrt はエラーを無出力にする。
var errPythonException = errors.New("the Python implementation raises on this input")

func run(c *hookrt.Context) error {
	command, response, cwd, event, err := read(c)
	if err != nil || event != "PostToolUse" {
		return err
	}
	if command == "" || !triggersPush(command) || !toolresponse.Succeeded(response) {
		return nil
	}
	pullRequest, err := fetch(cwd)
	if err != nil || pullRequest == nil {
		return err
	}
	headlines, err := staleCommits(pullRequest)
	if err != nil || len(headlines) == 0 {
		return err
	}
	var settings Settings
	// 設定の型が違っても（install が報告する）、既定の文面で注意は出す。
	_ = c.Settings(&settings)
	language := c.Language()
	instruction := settings.Instruction(language)
	text, err := message(language, pullRequest, headlines, instruction)
	if err != nil {
		return err
	}
	c.AddContext(event, text)
	return nil
}

// Instruction は hook と設定の表示が同じ更新案内を使うための解決済みの文面である。
func (s Settings) Instruction(language i18n.Language) string {
	if custom := strings.TrimSpace(s.UpdateInstruction); custom != "" {
		return terminated(language, custom)
	}
	return messages.T(language, idDefaultInstruction)
}

// read は判定の材料を読む。event が PostToolUse でなければ何もしない。
func read(c *hookrt.Context) (command string, response any, cwd, event string, err error) {
	if c.FromArgs {
		cwd, err := py.Getcwd()
		if err != nil {
			return "", nil, "", "", errPythonException
		}
		return string(c.Input), map[string]any{}, cwd, "PostToolUse", nil
	}
	// 移植元は stdin を UTF-8 として厳密に読むので、不正なバイト列は読み込みの例外で無出力になっていた。
	if !utf8.Valid(c.Input) {
		return "", nil, "", "", nil
	}
	payload := decodeObject(c.Input)
	event = "PostToolUse"
	if value := payload["hook_event_name"]; toolresponse.Truthy(value) {
		// 文字列でないイベント名は PostToolUse と一致しないので、移植元でも何もしない。
		text, _ := value.(string)
		event = text
	}
	// 移植元の (tool_input or {}).get("command") or ""。オブジェクトでない tool_input は例外になる。
	if toolInput := payload["tool_input"]; toolresponse.Truthy(toolInput) {
		fields, ok := toolInput.(map[string]any)
		if !ok {
			return "", nil, "", "", errPythonException
		}
		if value := fields["command"]; toolresponse.Truthy(value) {
			text, ok := value.(string)
			if !ok {
				// 移植元は push の判定（re.split）で例外になる。PostToolUse でなければその前に抜けるが、どちらも無出力である。
				return "", nil, "", "", errPythonException
			}
			command = text
		}
	}
	if value := payload["cwd"]; toolresponse.Truthy(value) {
		text, ok := value.(string)
		if !ok {
			// 移植元は git の起動で例外になる。git まで進まない入力（push でない・失敗した push）では無出力で同じになる。
			return "", nil, "", "", errPythonException
		}
		cwd = text
	} else if cwd, err = py.Getcwd(); err != nil {
		return "", nil, "", "", errPythonException
	}
	return command, payload["tool_response"], cwd, event, nil
}

// decodeObject は payload を JSON のオブジェクトとして読む。壊れているかオブジェクトでなければ空を返す。
func decodeObject(raw []byte) map[string]any {
	value, ok := decodeJSON(string(raw))
	payload, isObject := value.(map[string]any)
	if !ok || !isObject {
		return map[string]any{}
	}
	return payload
}

// decodeJSON は text 全体を 1 つの JSON の値として読む。数値は json.Number のまま持つ。
func decodeJSON(text string) (any, bool) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, false
	}
	// 値の後ろに空白以外が残っていれば、Python の json.loads と同じく壊れているとみなす。
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return value, true
}

var (
	// separatorRE はシェルのコマンド区切りである。改行も区切りにする（1 セグメントに複数のコマンドを混ぜない）。
	separatorRE   = regexp.MustCompile(`\|\||&&|[;|&\n]`)
	punctuationRE = regexp.MustCompile("[\"'`(){}\\[\\]]")
	redirectRE    = regexp.MustCompile(`[0-9]*[<>]+&?` + py.Space + `*` + py.NotSpace + `*`)
)

// cancellingFlags は push を打ち消すフラグである。--dry-run はリモートを変えず、削除系は PR を進めない。
var cancellingFlags = map[string]bool{"--dry-run": true, "-n": true, "--delete": true, "-d": true}

// triggersPush は PR の commit を増やす push が含まれるかを返す。
func triggersPush(command string) bool {
	for _, segment := range separatorRE.Split(command, -1) {
		cleaned := punctuationRE.ReplaceAllString(segment, " ")
		cleaned = redirectRE.ReplaceAllString(cleaned, " ")
		tokens := py.Fields(cleaned)
		isGit, hasPush, cancelled, deletes := false, false, false, false
		for _, token := range tokens {
			lower := py.Lower(token)
			isGit = isGit || lower == "git" || strings.HasSuffix(lower, "/git")
			hasPush = hasPush || lower == "push"
			cancelled = cancelled || cancellingFlags[lower]
			// `git push origin :branch` は削除。通常の refspec と違いコロンで始まる。
			deletes = deletes || strings.HasPrefix(token, ":")
		}
		if isGit && hasPush && !cancelled && !deletes {
			return true
		}
	}
	return false
}

// run1 は git か gh を実行し、stdout の両端の空白を除いて返す。失敗・時間切れ・0 以外の終了なら ok は偽になる。
func run1(cwd string, name string, args ...string) (string, bool) {
	output, ok := hookexec.Output(cwd, commandTimeout, name, args...)
	if !ok {
		return "", false
	}
	return py.Strip(output), true
}

// ownerRepo は origin が github.com なら owner と repo を返す。
func ownerRepo(cwd string) (string, string, bool) {
	url, ok := run1(cwd, "git", "config", "--get", "remote.origin.url")
	if !ok || url == "" {
		return "", "", false
	}
	return matchOrigin(url)
}

// originRE は移植元の ORIGIN_PATTERN から先頭の後読み (?<![A-Za-z0-9-]) を除いたものである。
// https://github.com/<owner>/<repo>(.git) と git@github.com:<owner>/<repo>(.git) に一致する。
// Python の IGNORECASE は i を ı（U+0131）と İ（U+0130）にも一致させるが、Go の (?i) は一致させないので、文字クラスで足す。
var originRE = regexp.MustCompile(`(?i)^g[i\x{131}\x{130}]thub\.com[/:]+([^/]+)/([^/]+?)(?:\.g[i\x{131}\x{130}]t)?/*$`)

// matchOrigin は移植元の ORIGIN_PATTERN.search(url) である。RE2 は後読みを扱えないので、
// ホスト名の直前が [A-Za-z0-9-] でない位置（notgithub.com を拾わない境界）ごとに、その位置から末尾までの一致を試す。
// パターンは末尾（$）まで一致させるので、開始位置が決まれば Python と同じ一致になる。最初に一致した位置の結果を返す。
func matchOrigin(url string) (string, string, bool) {
	for start := 0; start < len(url); start++ {
		if previous, _ := utf8.DecodeLastRuneInString(url[:start]); start > 0 && isHostChar(previous) {
			continue
		}
		// github.com の g は大文字小文字を無視して一致する。Python の IGNORECASE も ASCII の g・G だけを同じとみなす。
		if url[start] != 'g' && url[start] != 'G' {
			continue
		}
		if match := originRE.FindStringSubmatch(url[start:]); match != nil {
			return match[1], match[2], true
		}
	}
	return "", "", false
}

// isHostChar は後読みの [A-Za-z0-9-]（IGNORECASE）に一致する文字かを返す。
// Python の IGNORECASE は英字に K（U+212A）・ſ（U+017F）・ı（U+0131）・İ（U+0130）も一致させる。
func isHostChar(char rune) bool {
	switch {
	case char >= 'A' && char <= 'Z', char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '-':
		return true
	default:
		return char == 0x212A || char == 0x17F || char == 0x131 || char == 0x130
	}
}

// fetch は push した HEAD に紐づく open の PR を返す。取れなければ nil を返す。
func fetch(cwd string) (map[string]any, error) {
	owner, repo, ok := ownerRepo(cwd)
	if !ok {
		return nil, nil
	}
	sha, ok := run1(cwd, "git", "rev-parse", "HEAD")
	if !ok || sha == "" {
		return nil, nil
	}
	raw, ok := run1(cwd, "gh", "api", "graphql", "-f", "query="+query,
		"-F", "owner="+owner, "-F", "repo="+repo, "-F", "sha="+sha)
	if !ok || raw == "" {
		return nil, nil
	}
	payload, ok := decodeJSON(raw)
	if !ok {
		return nil, nil
	}
	nodes := payload
	for _, key := range []string{"data", "repository", "object", "associatedPullRequests", "nodes"} {
		fields, ok := nodes.(map[string]any)
		if !ok {
			return nil, nil
		}
		if nodes, ok = fields[key]; !ok {
			return nil, nil
		}
	}
	switch typed := nodes.(type) {
	case []any:
		for _, node := range typed {
			if fields, ok := node.(map[string]any); ok && fields["state"] == "OPEN" {
				return fields, nil
			}
		}
	case json.Number, bool:
		// 移植元は for で回せない値で例外になる（真の数値と真偽値）。偽の値は `or []` で空になる。
		if toolresponse.Truthy(typed) {
			return nil, errPythonException
		}
	}
	// オブジェクトと文字列は、回しても dict の要素が出てこないので見つからない。
	return nil, nil
}

// staleCommits は本文の最終更新より後の commit（merge commit を除く）の見出しを新しい順に返す。
// 見出しは移植元の (messageHeadline or "") のままの値で、文字列でないものは message で例外（無出力）にする。
func staleCommits(pullRequest map[string]any) ([]any, error) {
	written, ok := parseTime(pullRequest["lastEditedAt"])
	if !ok {
		if written, ok = parseTime(pullRequest["createdAt"]); !ok {
			return nil, nil
		}
	}
	commits, err := getOr(pullRequest["commits"], "nodes")
	if err != nil {
		return nil, err
	}
	nodes, isList := commits.([]any)
	if !isList && toolresponse.Truthy(commits) {
		// 配列以外の真の値は、回すと文字列（オブジェクトの鍵や文字）か例外になり、どちらも次の .get で例外になる。
		return nil, errPythonException
	}
	var newer []any
	for _, node := range nodes {
		commit, err := getOr(node, "commit")
		if err != nil {
			return nil, err
		}
		fields, isObject := commit.(map[string]any)
		if !isObject && toolresponse.Truthy(commit) {
			return nil, errPythonException
		}
		parents, err := getOr(fields["parents"], "totalCount")
		if err != nil {
			return nil, err
		}
		count, err := parentCount(parents)
		if err != nil {
			return nil, err
		}
		if count > 1 {
			continue
		}
		if committed, ok := parseTime(fields["committedDate"]); ok && committed.After(written) {
			headline := fields["messageHeadline"]
			if !toolresponse.Truthy(headline) {
				headline = ""
			}
			newer = append(newer, headline)
		}
	}
	for left, right := 0, len(newer)-1; left < right; left, right = left+1, right-1 {
		newer[left], newer[right] = newer[right], newer[left]
	}
	return newer, nil
}

// getOr は移植元の (value or {}).get(key) である。value が偽なら nil を、オブジェクトでない真の値なら例外を返す。
func getOr(value any, key string) (any, error) {
	if !toolresponse.Truthy(value) {
		return nil, nil
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return nil, errPythonException
	}
	return fields[key], nil
}

// parentCount は移植元の (... .get("totalCount") or 1) を数として返す。偽なら 1 とする。
func parentCount(value any) (float64, error) {
	if !toolresponse.Truthy(value) {
		return 1, nil
	}
	switch typed := value.(type) {
	case json.Number:
		number, err := strconv.ParseFloat(typed.String(), 64)
		if err != nil {
			return 0, errPythonException
		}
		return number, nil
	case bool:
		return 1, nil
	default:
		// 文字列・配列・オブジェクトと 1 の比較は Python では例外になる。
		return 0, errPythonException
	}
}

// timeRE は Python 3.14 の _strptime が "%Y-%m-%dT%H:%M:%S" から作る正規表現である（IGNORECASE）。
// 数字は Unicode の数字（\d）も受け付ける。
var timeRE = regexp.MustCompile(`^(?i)(` + py.Digit + `{4})-(1[0-2]|0[1-9]|[1-9])-(3[0-1]|[1-2]` + py.Digit +
	`|0[1-9]|[1-9]| [1-9])T(2[0-3]|[0-1]` + py.Digit + `|` + py.Digit + `| ` + py.Digit + `):([0-5]` + py.Digit + `|` +
	py.Digit + `):(6[0-1]|[0-5]` + py.Digit + `|` + py.Digit + `)$`)

// parseTime は GitHub の ISO8601（末尾 Z）を秒単位の時刻にする。読めなければ ok は偽になる。
// 移植元の datetime.strptime(value.rstrip("Z").split(".")[0], "%Y-%m-%dT%H:%M:%S") と同じ形を受け付ける。
func parseTime(value any) (time.Time, bool) {
	text, ok := value.(string)
	if !ok || text == "" {
		return time.Time{}, false
	}
	text = strings.TrimRight(text, "Z")
	if index := strings.IndexByte(text, '.'); index >= 0 {
		text = text[:index]
	}
	match := timeRE.FindStringSubmatch(text)
	if match == nil {
		return time.Time{}, false
	}
	fields := make([]int, 6)
	for index := range fields {
		fields[index] = digitsValue(strings.TrimLeft(match[index+1], " "))
	}
	year, month, day, hour, minute, second := fields[0], fields[1], fields[2], fields[3], fields[4], fields[5]
	// datetime の範囲外（0 年、存在しない日付、閏秒）は ValueError になる。
	if year < 1 || second > 59 {
		return time.Time{}, false
	}
	parsed := time.Date(year, time.Month(month), day, hour, minute, second, 0, time.UTC)
	if parsed.Day() != day || int(parsed.Month()) != month {
		return time.Time{}, false
	}
	return parsed, true
}

// digitsValue は Unicode の数字（Nd）の並びを 10 進数として読む（Python の int(str)）。
func digitsValue(digits string) int {
	value := 0
	for _, char := range digits {
		value = value*10 + digitValue(char)
	}
	return value
}

// digitValue は Nd の文字の値を返す。Nd の文字は 0 から 9 の 10 文字ずつの連続した並びで表に載っている。
func digitValue(char rune) int {
	if char >= '0' && char <= '9' {
		return int(char - '0')
	}
	for _, span := range unicode.Nd.R16 {
		if rune(span.Lo) <= char && char <= rune(span.Hi) && span.Stride == 1 {
			return int(char-rune(span.Lo)) % 10
		}
	}
	for _, span := range unicode.Nd.R32 {
		if rune(span.Lo) <= char && char <= rune(span.Hi) && span.Stride == 1 {
			return int(char-rune(span.Lo)) % 10
		}
	}
	return 0
}

// message は注入する注意を組み立てる。
// terminated は、文末の記号が無い案内に句点を足す。注入文では案内の直後に次の文が続き、句点が無いと 1 文につながるため。
func terminated(language i18n.Language, sentence string) string {
	last, _ := utf8.DecodeLastRuneInString(sentence)
	if strings.ContainsRune(sentenceEnders, last) {
		return sentence
	}
	return sentence + messages.T(language, idSentenceEnd)
}

func message(language i18n.Language, pullRequest map[string]any, headlines []any, instruction string) (string, error) {
	number, err := pyDecimal(pullRequest["number"])
	if err != nil {
		return "", err
	}
	shown := headlines[:min(len(headlines), maxHeadlines)]
	lines := make([]string, len(shown))
	for index, headline := range shown {
		line, ok := headline.(string)
		if !ok {
			// 移植元は "  - " + line で例外になる。
			return "", errPythonException
		}
		lines[index] = "  - " + line
	}
	listed := strings.Join(lines, "\n")
	if len(headlines) > maxHeadlines {
		listed += messages.Text(language, idMoreCommits, map[string]any{"Count": len(headlines) - maxHeadlines})
	}
	url := pullRequest["url"]
	if !toolresponse.Truthy(url) {
		url = ""
	}
	return messages.Text(language, idStale, map[string]any{
		"Number": number, "Count": len(headlines), "Commits": listed, "Instruction": instruction,
		"URL": toolresponse.PyStr(url),
	}), nil
}

// pyDecimal は移植元の "%d" % (number or 0) である。整数でも小数でもない真の値は例外になる。
func pyDecimal(value any) (string, error) {
	switch typed := value.(type) {
	case json.Number:
		text := typed.String()
		if !strings.ContainsAny(text, ".eE") {
			return strings.TrimPrefix(toolresponse.PyStr(typed), "+"), nil
		}
		number, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
			return "", errPythonException
		}
		return strconv.FormatFloat(math.Trunc(number), 'f', 0, 64), nil
	case bool:
		if typed {
			return "1", nil
		}
		return "0", nil
	case nil:
		return "0", nil
	case string:
		if typed == "" {
			return "0", nil
		}
	default:
		if !toolresponse.Truthy(typed) {
			return "0", nil
		}
	}
	return "", errPythonException
}
