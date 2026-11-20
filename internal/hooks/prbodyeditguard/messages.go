package prbodyeditguard

import (
	"strings"

	"github.com/HappyOnigiri/hhx/internal/i18n"
)

const (
	idReason   = "pr-body-edit-guard.reason"
	idHead     = "pr-body-edit-guard.head"
	idTail     = "pr-body-edit-guard.tail"
	idFinding  = "pr-body-edit-guard.finding"
	idANSI     = "pr-body-edit-guard.ansi"
	idGuidance = "pr-body-edit-guard.guidance"
	idLogs     = "pr-body-edit-guard.logs"
)

var messages = i18n.Register(i18n.Catalog{
	// 混入の疑いがある本文を送り出さず、確認済みの同じ本文にだけ使える ACK を案内する。
	idReason: {
		EN: "❌ Blocked: This PR edit may add terminal output to the body.\n" +
			"Reason:\n{{.Findings}}\n" +
			"Action: Check the added lines and remove any unintended output. " +
			"If the lines are intentional, retry the same edit with `{{.ACK}}` immediately before `gh pr edit`.",
		JA: "❌ Blocked: PR 本文に端末出力が混ざっている可能性があります。\n" +
			"Reason:\n{{.Findings}}\n" +
			"Action: 追加行を確認し、意図しない出力を取り除いてください。" +
			"意図した追記なら `gh pr edit` の直前に `{{.ACK}}` を付けて、同じ本文で再実行してください。",
	},
	// 本文のどちらの端に追加された行かを示す。
	idHead: {EN: "start of the body", JA: "本文の先頭"},
	// 本文のどちらの端に追加された行かを示す。
	idTail: {EN: "end of the body", JA: "本文の末尾"},
	// 送信候補の行範囲と、検知の根拠を示す。
	idFinding: {
		EN: "- {{.Edge}}, lines {{.Start}}–{{.End}}: {{.Why}}",
		JA: "- {{.Edge}}（{{.Start}}–{{.End}} 行目）に{{.Why}}",
	},
	// 生の端末色制御文字の混入を示す。
	idANSI: {EN: "contains an ANSI color escape sequence", JA: "ANSI 色制御シーケンスがあります。"},
	// ターミナルから出た案内の構造があることを示す。
	idGuidance: {
		EN: "a diagnostic label is followed by an indented command and usage instructions",
		JA: "診断ラベル、インデントされたコマンド、使い方の案内が続いています。",
	},
	// 同じツールの診断ログが連続していることを示す。
	idLogs: {EN: "consecutive diagnostic lines share a tool prefix", JA: "同じツール名で始まる診断ログが連続しています。"},
})

func reason(language i18n.Language, findings []finding, digest string) string {
	var lines []string
	for _, f := range findings {
		lines = append(lines, messages.Text(language, idFinding, map[string]any{
			"Edge": messages.T(language, f.edge), "Start": f.start, "End": f.end, "Why": messages.T(language, f.id),
		}))
	}
	return messages.Text(language, idReason, map[string]any{
		"Findings": strings.Join(lines, "\n"), "ACK": ackVariable + "=" + digest,
	})
}
