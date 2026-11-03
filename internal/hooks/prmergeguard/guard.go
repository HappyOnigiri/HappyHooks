// Package prmergeguard は PR のマージにつながる操作を PreToolUse で拒否する hook（pr-merge-guard）である。
//
// 方針:
//   - 標準的な表記ゆれ（--method=PUT / -XPUT など）は塞ぐ。
//   - gh api は HTTP メソッドではなく REST エンドポイントで判定する。メソッドで一律に塞ぐと、
//     レビューコメントの編集など無関係な更新まで止まる。
//   - 変数展開・eval・別シェル経由（sh -c '...'）といった明示的な回避は対象外（README の既知の限界）。
//   - force push・リモートブランチの削除・保護ブランチへの push は通す（利用者の判断で日常的に使うため）。
//
// 開錠: AGENT_ALLOW_PR_MERGE=1 を付けて起動したエージェントの CLI からの呼び出しは、すべて通す。
// hook は CLI のプロセスの環境変数を継承するので、エージェントが Bash で export しても自分では開錠できない。
package prmergeguard

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"

	"github.com/HappyOnigiri/hhx/internal/hookrt"
	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

// Name は hook の名前である。
const Name = "pr-merge-guard"

// UnlockEnv は開錠の環境変数である。値が "1" のときだけ開錠する。
const UnlockEnv = "AGENT_ALLOW_PR_MERGE"

// Definition は pr-merge-guard の定義を返す。
func Definition() hookrt.Definition {
	return hookrt.Definition{
		Name:           Name,
		DefaultEnabled: true,
		Registrations: []hookrt.Registration{
			{Agent: hookrt.Claude, Event: "PreToolUse", Matcher: "Bash"},
			{Agent: hookrt.Codex, Event: "PreToolUse", Matcher: "Bash"},
		},
		Gate: gate,
		Run:  run,
	}
}

// gateKeywords は一次ゲートの語である。これを含まない入力は判定しない（テストで固定した挙動）。
var gateKeywords = []string{"git", "gh", "curl", "wget"}

func gate(input []byte) bool {
	// 開錠済みなら何も判定しない。一次ゲートより前に見る。
	if os.Getenv(UnlockEnv) == "1" {
		return false
	}
	raw := string(input)
	for _, keyword := range gateKeywords {
		if strings.Contains(raw, keyword) {
			return true
		}
	}
	return false
}

func run(c *hookrt.Context) error {
	command := string(c.Input)
	if !c.FromArgs {
		command = commandFrom(c.Input)
	}
	if command == "" {
		return nil
	}
	if target := evaluate(command); target != "" {
		c.Deny(reason(c.Language(), target))
	}
	return nil
}

// commandFrom は PreToolUse の payload から実行するコマンドを取り出す。
// JSON として読めない入力と object でない JSON は、fail-open にせず生の文字列のまま判定に回す。
func commandFrom(raw []byte) string {
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return string(raw)
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return string(raw)
	}
	toolInput, ok := object["tool_input"].(map[string]any)
	if !ok {
		return ""
	}
	// 文字列でない command は、移植元では判定の途中で例外になり無出力で終わっていた。
	command, _ := toolInput["command"].(string)
	return command
}

const (
	// rtk ラッパー経由の呼び出しも同じ扱いにする。
	rtk = `(?:rtk` + py.Space + `+)?`
	// 同じコマンドの中（パイプ・コマンド区切りを越えない）。
	seg = `[^|;&]*`
	// コマンドの先頭の境界。シェルの区切り（`git status;gh pr merge 1`）も拾う。
	// クォートは含めない（含めると `echo 'gh pr merge'` のような無害な引用まで塞ぐ）。
	boundary = `(?:^|[;&|(]|` + py.Space + `)`
	// /usr/local/bin/gh のようなパス指定の起動も同じ扱いにする。
	pathPrefix = `(?:` + py.NotSpace + `*/)?`
	// マージ用の REST エンドポイント（PR のマージとブランチのマージ）。メソッドは問わない。
	// 末尾に英数字以外を要求して、branches/feature/merge-fix/... のような「merge を語の一部に含むだけのパス」を通す。
	mergeEndpoint = `(?:pulls/` + py.Digit + `+/merge|repos/[^/` + py.SpaceChars + `]+/[^/` + py.SpaceChars +
		`]+/merges)(?:[^A-Za-z0-9_-]|$)`
)

var (
	// 二次ゲート: 実際のコマンドに語の境界つきで判定し直す（"legit" の git を拾わない）。
	secondGateRE = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])(?:git|gh|curl|wget)(?:[^A-Za-z0-9_]|$)`)
	methodFlagRE = regexp.MustCompile(`--method[=` + py.SpaceChars + `]+`)
	joinedXFlag  = regexp.MustCompile(`(^|` + py.Space + `)-X([A-Za-z]+)`)

	prMergeRE = regexp.MustCompile(boundary + rtk + pathPrefix + `gh` + py.Space + `+pr` + py.Space +
		`+merge(?:` + py.Space + `|$)`)
	apiMergeRE = regexp.MustCompile(boundary + rtk + pathPrefix + `gh` + py.Space + `+api` + seg + mergeEndpoint)
	graphQLRE  = regexp.MustCompile(`(?i)mergePullRequest|enablePullRequestAutoMerge`)
	httpHostRE = regexp.MustCompile(`(?:curl|wget)` + seg + `api\.github\.com`)
	httpWrite  = regexp.MustCompile(`-X` + py.Space + `+(?:PUT|PATCH|POST)|/merge`)
)

// normalize は表記ゆれをそろえる。--method=PUT / --method PUT / -XPUT をすべて "-X PUT" に寄せ、改行を空白にする。
func normalize(command string) string {
	normalized := strings.ReplaceAll(command, "\n", " ")
	normalized = methodFlagRE.ReplaceAllString(normalized, "-X ")
	return joinedXFlag.ReplaceAllString(normalized, "${1}-X ${2}")
}

// evaluate はコマンドを判定し、拒否するなら発火したルールの ID（対象の表記のカタログの ID）を、通すなら空文字列を返す。
// ルールはこの順に見て、最初に一致したものを返す。
func evaluate(command string) string {
	if !secondGateRE.MatchString(command) {
		return ""
	}
	normalized := normalize(command)
	switch {
	case prMergeRE.MatchString(normalized):
		return idTargetPRMerge
	case apiMergeRE.MatchString(normalized):
		return idTargetAPI
	case graphQLRE.MatchString(normalized):
		return idTargetGraphQL
	case httpHostRE.MatchString(normalized) && httpWrite.MatchString(normalized):
		return idTargetHTTP
	default:
		return ""
	}
}
