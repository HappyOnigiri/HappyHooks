package forbiddentermguard

import (
	"strconv"
	"strings"

	"github.com/HappyOnigiri/hhx/internal/i18n"
)

// 理由文は「該当箇所・なぜ・どうする」を返す。回避を思いとどまらせるのはこの文面だけなので、
// 語リストの編集や、同じ語を形を変えて送る迂回（綴りを崩す・別コマンドや別ファイル経由）をしないことを対応に書く。
const (
	idReason        = "forbidden-term-guard.reason"
	idMore          = "forbidden-term-guard.more"
	idWhy           = "forbidden-term-guard.why"
	idHow           = "forbidden-term-guard.how"
	idInvalidReason = "forbidden-term-guard.invalid.reason"
	idInvalidHow    = "forbidden-term-guard.invalid.how"
	idLineSeparator = "forbidden-term-guard.invalid.line-separator"
)

var messages = i18n.Register(i18n.Catalog{
	// 禁止語の該当箇所を並べた理由文。該当箇所・理由・対応の順に出す。
	idReason: {
		EN: "❌ Blocked: sending text that contains a forbidden term\n\nFound in:\n{{.Findings}}\n\n" +
			"Reason: {{.Why}} (term list: {{.Path}})\n\nAction: {{.How}}",
		JA: "❌ ブロック: 禁止語を含む本文の送信\n\n該当箇所:\n{{.Findings}}\n\n" +
			"理由: {{.Why}} (語リスト: {{.Path}})\n\n対応: {{.How}}",
	},
	// 載せきれなかった該当箇所の件数の行。
	idMore: {EN: "  … {{.Count}} more", JA: "  … 他 {{.Count}} 件"},
	// 禁止語リストの目的（他のリポジトリやサービスの名前を公開先へ持ち出さない）を伝える。
	idWhy: {
		EN: "The text contains a term from this repository's forbidden term list. " +
			"The list keeps the names of other repositories and services out of public destinations.",
		JA: "このリポジトリの禁止語リストに載っている語が本文に含まれています。" +
			"他リポジトリやサービスの名称を公開先へ持ち出さないための設定です。",
	},
	// 語を取り除いて再実行させる。語リストの編集や、同じ語を形を変えて送る迂回を禁じ、無理ならユーザーに判断を仰がせる。
	idHow: {
		EN: "Remove the term from the text and run the command again; you do not need to ask the user. " +
			"Do not edit or delete the term list, and do not send the same term in another form to get around " +
			"this block, such as a broken spelling, an encoding, or a different command or file. If removing the term " +
			"would break the meaning, tell the user what you want to write and how, and ask for a decision.",
		JA: "該当箇所から語を取り除いて実行し直してください。ユーザーへの確認は不要です。" +
			"語リストの編集・削除や、綴りを崩す・エンコードする・別コマンドや別ファイル経由で送るなど" +
			"形を変えて同じ語を送ることはしないでください。取り除くと意味が通らなくなる場合は、" +
			"何をどう書きたいかをユーザーに伝えて判断を仰いでください。",
	},
	// 語リストに RE2 で使えない re: の行があり、検査できないときの理由文。
	idInvalidReason: {
		EN: "❌ Blocked: sending text that cannot be checked for forbidden terms\n\n" +
			"Reason: a re: line in the forbidden term list uses syntax that hhx's regular expressions (RE2) " +
			"do not support, so that term cannot be checked. (term list: {{.Path}}, line(s) {{.Lines}})\n\n" +
			"Action: {{.How}}",
		JA: "❌ ブロック: 禁止語を検査できない本文の送信\n\n" +
			"理由: 禁止語リストの re: の行に、hhx の正規表現（RE2）で使えない構文があり、その語を検査できません。" +
			" (語リスト: {{.Path}} の {{.Lines}} 行目)\n\n対応: {{.How}}",
	},
	// 語リストの修正をユーザーに依頼させる。語リストの編集や、同じ本文を形を変えて送る迂回を禁じる。
	idInvalidHow: {
		EN: "Ask the user to fix those lines of the term list to the syntax hhx supports (docs/forbidden-terms.md). " +
			"Do not edit or delete the term list, and do not send the same text in another form " +
			"(a different command or file) to get around this block.",
		JA: "語リストの該当行を hhx で使える構文（docs/forbidden-terms.md）に直すよう、" +
			"ユーザーに依頼してください。語リストの編集・削除や、別コマンド・別ファイル経由など" +
			"形を変えて同じ本文を送ることはしないでください。",
	},
	// 行番号を並べるときの区切り。
	idLineSeparator: {EN: ", ", JA: "・"},
})

// reason は理由文を組み立てる。該当箇所は最大 maxReported 件まで出し、残りは件数だけを示す。
func reason(language i18n.Language, findings []string, termsPath string) string {
	shown := findings
	if len(shown) > maxReported {
		shown = shown[:maxReported]
	}
	detail := strings.Join(shown, "\n")
	if remaining := len(findings) - maxReported; remaining > 0 {
		detail += "\n" + messages.Text(language, idMore, map[string]any{"Count": remaining})
	}
	return messages.Text(language, idReason, map[string]any{
		"Findings": detail,
		"Why":      messages.T(language, idWhy),
		"Path":     termsPath,
		"How":      messages.T(language, idHow),
	})
}

// invalidReason は、語リストにコンパイルできない re: の行があって検査できないときの理由文を組み立てる。
func invalidReason(language i18n.Language, lines []int, termsPath string) string {
	numbers := make([]string, len(lines))
	for index, line := range lines {
		numbers[index] = strconv.Itoa(line)
	}
	return messages.Text(language, idInvalidReason, map[string]any{
		"Path":  termsPath,
		"Lines": strings.Join(numbers, messages.T(language, idLineSeparator)),
		"How":   messages.T(language, idInvalidHow),
	})
}
