// Package pushcicontext は、push・PR 作成・workflow dispatch の成功後に CI の待ち方をコンテキストへ注入する hook
// （push-ci-context）である。
//
// 狙いは 1 つだけで、CI の結果を見ないまま作業を終えたり、短いポーリングのたびにモデルへ制御を戻したりするのを避ける。
// ただしこの案内が他の残作業（PR 本文の更新、別 PR の改修など）を押しのけないよう、待機はターンの最後に回させる。
//
// PreToolUse ではなく PostToolUse に登録する。実行前だと拒否された push や非 fast-forward で失敗した push にも案内が出るうえ、
// 注入の位置がツールの結果の直後になり、次の行動を決める場所と一致するためである。
// Claude Code の PostToolUse は成功時だけ発火するが、他の CLI の挙動は保証が無いので、出力の側でも失敗を弾く。
// PreToolUse で起動されたときの経路（「成功したら」の文面）も持つ。
//
// 発火しない条件（誤爆を避ける）:
//   - --dry-run / -n の push、ブランチの削除（--delete / -d / `:ref` の形）
//   - `gh workflow run --help`
//   - Everything up-to-date（新しい commit が無く CI が動かない）
//   - push / PR 作成で、結果が GitHub を指しておらず、cwd の origin が github.com でないと確認できた場合。
//     判定できないときは注入する（Codex は exec の workdir を hook に渡さず、cwd がセッション開始時のままになるため）。
//
// デバッグ経路（`hhx hook push-ci-context '<コマンド文字列>'`）の引数はコマンド文字列で、PostToolUse の成功として扱う。
package pushcicontext

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HappyOnigiri/hhx/internal/hookexec"
	"github.com/HappyOnigiri/hhx/internal/hookrt"
	"github.com/HappyOnigiri/hhx/internal/i18n"
	py "github.com/HappyOnigiri/hhx/internal/pycompat"
	"github.com/HappyOnigiri/hhx/internal/toolresponse"
)

// Name は hook の名前である。
const Name = "push-ci-context"

// Definition は push-ci-context の定義を返す。
func Definition() hookrt.Definition {
	return hookrt.Definition{
		Name:           Name,
		DefaultEnabled: true,
		Registrations: []hookrt.Registration{
			{Agent: hookrt.Claude, Event: "PostToolUse", Matcher: "Bash", Timeout: 10},
			{Agent: hookrt.Codex, Event: "PostToolUse", Matcher: "Bash", Timeout: 10, AdditionalContextLimit: 4096},
		},
		Gate: gate,
		Run:  run,
	}
}

// gitTimeout は origin を調べる git の上限である。
const gitTimeout = 5 * time.Second

// gate は CI を起こすコマンドの語を含まない入力を抜ける（大半はここで終わる）。
func gate(input []byte) bool {
	lower := py.Lower(string(input))
	return strings.Contains(lower, "push") ||
		(strings.Contains(lower, "pr") && strings.Contains(lower, "create")) ||
		(strings.Contains(lower, "workflow") && strings.Contains(lower, "run"))
}

// errPythonException は、移植元では例外になって無出力で終わっていた入力を表す。hookrt はエラーを無出力にする。
var errPythonException = errors.New("the Python implementation raises on this input")

// now は workflow run の一覧を絞る時刻の基準である。テストで差し替える。
var now = time.Now

type kind int

const (
	none kind = iota
	pushOrPR
	workflowDispatch
)

// invocation は payload から読んだ判定の材料である。
type invocation struct {
	command  string
	response any
	cwd      string
	// cwdInvalid は cwd が文字列でなかったことを表す。移植元は git の起動で例外になる。
	cwdInvalid bool
	event      string
	codex      bool
}

func run(c *hookrt.Context) error {
	input, err := read(c)
	if err != nil || input == nil {
		return err
	}
	if input.event != "PostToolUse" && input.event != "PreToolUse" {
		return nil
	}
	trigger := triggerKind(input.command)
	if input.command == "" || trigger == none {
		return nil
	}
	if input.event == "PostToolUse" && !toolresponse.Succeeded(input.response) {
		return nil
	}
	if trigger == pushOrPR && isMainPush(input) {
		return nil
	}
	// workflow dispatch は `-R owner/repo` で Git リポジトリの外からも実行できる。
	// push / PR 作成は、結果が GitHub を指していればその時点で確定する。指していない場合だけ cwd を見る。
	if trigger == pushOrPR && !strings.Contains(py.Lower(toolresponse.Text(input.response)), "github.com") {
		if input.cwdInvalid {
			return errPythonException
		}
		if isNonGitHubRepository(input.cwd) {
			return nil
		}
	}
	c.AddContext(input.event, guidanceText(c.Language(), input, trigger))
	return nil
}

// guidanceText は注入する案内を組み立てる。書き出し・待ち方の方針・CLI ごとの待ち方の順に並べる。
func guidanceText(language i18n.Language, input *invocation, trigger kind) string {
	post := input.event == "PostToolUse"
	order := messages.T(language, idOrder)
	var context, label, command, body string
	if trigger == workflowDispatch {
		command = workflowWatchCommand(language, input.command, input.response)
		body = messages.Text(language, idDispatchGuidance, map[string]any{"Order": order})
		label = messages.T(language, idLabelDispatch)
		context = messages.T(language, idPreDispatch)
		if post {
			context = messages.T(language, idPostDispatch)
		}
	} else {
		command = waitCommand
		body = messages.Text(language, idGuidance, map[string]any{"Command": waitCommand, "Order": order})
		label = messages.T(language, idLabelWaitCI)
		context = messages.T(language, idPrePush)
		if post {
			context = messages.T(language, idPostPush)
		}
	}
	wait := messages.Text(language, idBash, map[string]any{"Command": command})
	if input.codex {
		wait = messages.Text(language, idCodex, map[string]any{"Label": label, "Command": py.QuoteJSON(command)})
	}
	return context + body + wait
}

// read は判定の材料を読む。nil を返したら無出力で終える。
func read(c *hookrt.Context) (*invocation, error) {
	if c.FromArgs {
		cwd, err := py.Getcwd()
		if err != nil {
			return nil, errPythonException
		}
		return &invocation{command: string(c.Input), response: map[string]any{}, cwd: cwd, event: "PostToolUse"}, nil
	}
	// 移植元は stdin を UTF-8 として厳密に読むので、不正なバイト列は読み込みの例外で無出力になっていた。
	if !utf8.Valid(c.Input) {
		return nil, nil
	}
	payload := decodeObject(c.Input)
	input := &invocation{response: payload["tool_response"], event: "PostToolUse"}
	// 移植元の (tool_input or {}).get("command") or ""。オブジェクトでない tool_input と文字列でない command は例外になる。
	if toolInput := payload["tool_input"]; toolresponse.Truthy(toolInput) {
		fields, ok := toolInput.(map[string]any)
		if !ok {
			return nil, errPythonException
		}
		if command := fields["command"]; toolresponse.Truthy(command) {
			text, ok := command.(string)
			if !ok {
				return nil, errPythonException
			}
			input.command = text
		}
	}
	if cwd := payload["cwd"]; toolresponse.Truthy(cwd) {
		text, ok := cwd.(string)
		input.cwd, input.cwdInvalid = text, !ok
	} else if input.cwd, _ = py.Getcwd(); input.cwd == "" {
		return nil, errPythonException
	}
	if event := payload["hook_event_name"]; toolresponse.Truthy(event) {
		// 文字列でないイベント名は、移植元では対応表に無い（または辞書の鍵にできず例外になる）ので無出力になる。
		text, ok := event.(string)
		if !ok {
			return nil, nil
		}
		input.event = text
	}
	input.codex = isCodex(payload)
	return input, nil
}

// decodeObject は payload を JSON のオブジェクトとして読む。壊れているかオブジェクトでなければ空を返す。
func decodeObject(raw []byte) map[string]any {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return map[string]any{}
	}
	// 値の後ろに空白以外が残っていれば、Python の json.loads と同じく壊れているとみなす。
	if _, err := decoder.Token(); err != io.EOF {
		return map[string]any{}
	}
	payload, ok := value.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return payload
}

// isCodex は transcript の名前（rollout-*-<session_id>.jsonl）で Codex かを判定する。
// 環境変数は親の CLI の値を継承し得るため使わない。
func isCodex(payload map[string]any) bool {
	sessionID, ok := payload["session_id"].(string)
	if !ok || sessionID == "" {
		return false
	}
	transcript, ok := payload["transcript_path"].(string)
	if !ok {
		return false
	}
	name := transcript[strings.LastIndex(transcript, "/")+1:]
	return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, "-"+sessionID+".jsonl")
}

const (
	shellWhitespace  = " \t\r"
	shellPunctuation = ";&|\n"
)

// commandSegments はコマンドを単純コマンドのトークン列に分ける。
// Python の shlex.shlex(command, posix=True, punctuation_chars=";&|\n")（空白は空白・タブ・CR だけ、コメントなし）で
// 分け、区切りの文字だけからなるトークンで切る。引用が閉じないときは何も返さない。
func commandSegments(command string) [][]string {
	tokens, err := py.ShlexSplit(command, shellWhitespace, shellPunctuation)
	if err != nil {
		return nil
	}
	var segments [][]string
	var current []string
	for _, token := range tokens {
		if token != "" && strings.Trim(token, shellPunctuation) == "" {
			if len(current) > 0 {
				segments = append(segments, current)
				current = nil
			}
			continue
		}
		current = append(current, token)
	}
	if len(current) > 0 {
		segments = append(segments, current)
	}
	return segments
}

var assignmentRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// executable は単純コマンドの実行ファイル名（先頭の代入を飛ばした basename）である。引数の中の `gh workflow run` は拾わない。
func executable(tokens []string) string {
	for _, token := range tokens {
		if assignmentRE.MatchString(token) {
			continue
		}
		return token[strings.LastIndex(token, "/")+1:]
	}
	return ""
}

func lowered(tokens []string) []string {
	out := make([]string, len(tokens))
	for index, token := range tokens {
		out[index] = py.Lower(token)
	}
	return out
}

func hasAdjacent(tokens []string, first, second string) bool {
	lower := lowered(tokens)
	for index := 0; index+1 < len(lower); index++ {
		if lower[index] == first && lower[index+1] == second {
			return true
		}
	}
	return false
}

func contains(tokens []string, values ...string) bool {
	for _, token := range tokens {
		for _, value := range values {
			if token == value {
				return true
			}
		}
	}
	return false
}

// cancellingFlags は push を打ち消すフラグである。--dry-run は CI を起こさず、削除系は対象の CI を持たない。
var cancellingFlags = []string{"--dry-run", "-n", "--delete", "-d"}

// gitPushIndex は git のサブコマンドが push のとき、その位置を返す。
// 引数全体から探すと stash push やオプションの値を誤認するため、大域オプションだけを飛ばす。
func gitPushIndex(tokens []string) int {
	index := 0
	for index < len(tokens) && assignmentRE.MatchString(tokens[index]) {
		index++
	}
	if index == len(tokens) || executable(tokens) != "git" {
		return -1
	}
	for index++; index < len(tokens); index++ {
		token := tokens[index]
		switch token {
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace", "--config-env":
			index++
			continue
		}
		if strings.HasPrefix(token, "-") {
			continue
		}
		if py.Lower(token) == "push" {
			return index
		}
		return -1
	}
	return -1
}

// triggerKind は CI を起こす操作の種別を返す。
func triggerKind(command string) kind {
	for _, tokens := range commandSegments(command) {
		lower := lowered(tokens)
		name := executable(tokens)
		if gitPushIndex(tokens) >= 0 {
			// 打ち消しフラグは push にだけ効かせる。gh pr create の -d は draft で、draft の PR でも CI は動く。
			if contains(lower, cancellingFlags...) {
				continue
			}
			// `git push origin :branch` は削除。`refs/x:refs/y` の通常の refspec と違い、コロンで始まるトークンになる。
			if hasColonPrefix(tokens) {
				continue
			}
			return pushOrPR
		}
		if name != "gh" {
			continue
		}
		if hasAdjacent(tokens, "pr", "create") {
			return pushOrPR
		}
		if hasAdjacent(tokens, "workflow", "run") && !contains(lower, "--help", "-h") {
			return workflowDispatch
		}
	}
	return none
}

var pushedRefRE = regexp.MustCompile(`(?m)\b\S+\s+->\s+(\S+)`)

// isMainPush は push 先が main だけだと確認できた場合に限り案内を止める。
// push の結果があれば送信先を優先し、結果から読めなければ refspec と現在のブランチを使う。
func isMainPush(input *invocation) bool {
	if input.event == "PostToolUse" {
		matches := pushedRefRE.FindAllStringSubmatch(toolresponse.Text(input.response), -1)
		if len(matches) > 0 {
			for _, match := range matches {
				if match[1] != "main" && match[1] != "refs/heads/main" {
					return false
				}
			}
			return true
		}
	}
	for _, tokens := range commandSegments(input.command) {
		push := gitPushIndex(tokens)
		if push < 0 {
			continue
		}
		var positional []string
		for _, token := range tokens[push+1:] {
			if !strings.HasPrefix(token, "-") {
				positional = append(positional, token)
			}
		}
		if len(positional) >= 2 {
			for _, refspec := range positional[1:] {
				destination := refspec
				if _, after, ok := strings.Cut(refspec, ":"); ok {
					destination = after
				}
				if destination != "main" && destination != "refs/heads/main" && destination != "HEAD" {
					return false
				}
				if destination == "HEAD" && !currentMainBranch(input.cwd) {
					return false
				}
			}
			return true
		}
		return currentMainBranch(input.cwd)
	}
	return false
}

func currentMainBranch(cwd string) bool {
	branch, ok := hookexec.Output(cwd, gitTimeout, "git", "symbolic-ref", "-q", "--short", "HEAD")
	return ok && py.Strip(branch) == "main"
}

func hasColonPrefix(tokens []string) bool {
	for _, token := range tokens {
		if strings.HasPrefix(token, ":") {
			return true
		}
	}
	return false
}

// workflowTokens は実際に `gh workflow run` を起動する最初の単純コマンドを返す。
// triggerKind と違い、--help / -h は小文字にせずに見る（移植元と同じ）。
func workflowTokens(command string) []string {
	for _, tokens := range commandSegments(command) {
		if executable(tokens) == "gh" && hasAdjacent(tokens, "workflow", "run") && !contains(tokens, "--help", "-h") {
			return tokens
		}
	}
	return nil
}

// optionValue は `-R value`・`-Rvalue`・`--repo=value` の形のオプションの値を返す。
func optionValue(tokens []string, short, long string) (string, bool) {
	for index, token := range tokens {
		if (token == short || token == long) && index+1 < len(tokens) {
			return tokens[index+1], true
		}
		if strings.HasPrefix(token, short) && token != short {
			return token[len(short):], true
		}
		if strings.HasPrefix(token, long+"=") {
			return token[len(long)+1:], true
		}
	}
	return "", false
}

var valueFlags = map[string]bool{
	"-R": true, "--repo": true, "-r": true, "--ref": true, "-f": true, "--raw-field": true, "-F": true, "--field": true,
}

// workflowName は `workflow run` の後ろの最初の位置引数を返す。
func workflowName(tokens []string) (string, bool) {
	lower := lowered(tokens)
	runIndex := -1
	for index := 0; index+1 < len(lower); index++ {
		if lower[index] == "workflow" && lower[index+1] == "run" {
			runIndex = index + 1
			break
		}
	}
	if runIndex < 0 {
		return "", false
	}
	for index := runIndex + 1; index < len(tokens); {
		token := tokens[index]
		switch {
		case valueFlags[token]:
			index += 2
		case token == "--json", strings.HasPrefix(token, "-"):
			index++
		default:
			return token, true
		}
	}
	return "", false
}

var runURLRE = regexp.MustCompile(`/actions/runs/([0-9]+)(?:[/?#]|` + py.Space + `|$)`)

// workflowRunID は `gh workflow run` が返した Actions の run の URL から ID を得る。
func workflowRunID(response any) (string, bool) {
	if _, ok := response.(map[string]any); !ok {
		return "", false
	}
	match := runURLRE.FindStringSubmatch(toolresponse.Text(response))
	if match == nil {
		return "", false
	}
	return match[1], true
}

// workflowWatchCommand は、dispatch した run を同じプロセスの中で登録待ちしてから watch するコマンドを返す。
func workflowWatchCommand(language i18n.Language, command string, response any) string {
	tokens := workflowTokens(command)
	repoArgs := ""
	if repository, ok := optionValue(tokens, "-R", "--repo"); ok && repository != "" {
		repoArgs = " -R " + py.ShlexQuote(repository)
	}
	if runID, ok := workflowRunID(response); ok {
		return "gh run watch " + runID + " --exit-status" + repoArgs
	}
	filters := []string{"--event workflow_dispatch"}
	if workflow, ok := workflowName(tokens); ok && workflow != "" {
		filters = append(filters, "--workflow "+py.ShlexQuote(workflow))
	}
	if ref, ok := optionValue(tokens, "-r", "--ref"); ok && ref != "" {
		filters = append(filters, "--branch "+py.ShlexQuote(ref))
	}
	cutoff := now().UTC().Add(-time.Minute).Format("2006-01-02T15:04:05Z")
	listCommand := "gh run list " + strings.Join(filters, " ") +
		" --limit 20 --json databaseId,createdAt" +
		" --jq " + py.ShlexQuote(`map(select(.createdAt >= "`+cutoff+`")) | .[].databaseId`) +
		repoArgs
	return "run_id=''; candidate=''; stable=0; attempts=0; " +
		`while [ -z "$run_id" ]; do ` +
		"run_ids=$(" + listCommand + "); list_rc=$?; " +
		`[ "$list_rc" -eq 0 ] || exit "$list_rc"; set -- $run_ids; ` +
		`if [ "$#" -gt 1 ]; then echo '` + messages.T(language, idMultipleCandidates) + `' >&2; exit 2; fi; ` +
		`if [ "$#" -eq 1 ]; then if [ "$candidate" = "$1" ]; then stable=$((stable + 1)); ` +
		"else candidate=$1; stable=1; fi; else candidate=''; stable=0; fi; " +
		`if [ "$stable" -ge 2 ]; then run_id=$candidate; break; fi; ` +
		`attempts=$((attempts + 1)); if [ "$attempts" -ge 180 ]; then ` +
		`echo '` + messages.T(language, idRegistrationTimeout) + `' >&2; exit 124; fi; sleep 30; done; ` +
		`gh run watch "$run_id" --exit-status` + repoArgs
}

// isNonGitHubRepository は cwd が GitHub 以外のリポジトリだと確認できたかを返す。判定できなければ偽を返す。
// CI を待ち損ねる方が誤注入より損失が大きいため、「GitHub ではない」と確認できたときだけ抑止する。
func isNonGitHubRepository(cwd string) bool {
	output, ok := hookexec.Output(cwd, gitTimeout, "git", "config", "--get", "remote.origin.url")
	if !ok {
		return false
	}
	origin := py.Lower(py.Strip(output))
	return origin != "" && !strings.Contains(origin, "github.com")
}
