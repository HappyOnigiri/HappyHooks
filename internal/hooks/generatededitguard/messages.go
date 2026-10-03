package generatededitguard

import (
	"strings"

	"github.com/HappyOnigiri/hhx/internal/i18n"
	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

// 理由文は「何を・なぜ・どうする」の 3 段を返す。回避を思いとどまらせるのはこの文面だけなので、生成コマンドでの作り直しへ誘導し、
// 別の手段での書き込みを禁じる。自律開発が主な用途なので、ユーザーへの確認や指示待ちは促さない。
// 宣言（スキップ）は、ユーザーが会話の中で明示的に指示した場合に限ると書く。
const (
	idReason = "generated-edit-guard.reason"
	// 止めた対象。手で編集しようとした生成ファイルの一覧を出す。
	idTarget = "generated-edit-guard.target"
	// 止める理由。マーカーがあり、手編集は次の生成で上書きされ、生成元とも食い違うことを伝える。
	idWhy = "generated-edit-guard.why"
	// 対応（生成コマンドが読めたとき）。生成元を直し、そのコマンドで作り直すよう命じる。
	idHowCommand = "generated-edit-guard.how.command"
	// 対応（生成コマンドが読めないとき）。生成元を直し、マーカーに書かれた生成器で作り直すよう命じる。
	idHowGenerator = "generated-edit-guard.how.generator"
	// 生成コマンドを実行できないときの指示。手で書かずに作業を続け、必要だった変更を最終報告に書かせる。別の手段での書き込みを禁じる。
	idHowFallback = "generated-edit-guard.how.fallback"
	// 宣言の案内。ユーザーが明示的に指示した場合に限り、宣言してから編集をやり直すよう伝える。
	idHowDeclare = "generated-edit-guard.how.declare"
	// 宣言のコマンドの例で、理由の引数の位置に置く説明。二重引用符で囲んで出すので、" $ ` \ を含めない。
	idDeclarePlaceholder = "generated-edit-guard.declare-placeholder"

	// 宣言を受け付けたことを伝える。
	idContextDeclared = "generated-edit-guard.context.declared"
	// 宣言に基づいて編集を通したことを伝える。
	idContextAllowed = "generated-edit-guard.context.allowed"
	// 最終報告に、編集した生成ファイル・理由とユーザーの指示・作り直しが要ることの 3 点を必ず載せるよう命じる。
	idContextReport = "generated-edit-guard.context.report"
	// 変数などで値が決まらず記録できなかった宣言のパスを伝え、リテラルで宣言し直すよう促す。
	idContextUnrecorded = "generated-edit-guard.context.unrecorded"
	// 生成コマンドが読めないときに、コマンドの代わりに置く語。
	idUnknownCommand = "generated-edit-guard.unknown-command"
	// 一覧の上限を超えた分を件数だけにした部分。
	idMore = "generated-edit-guard.more"
)

var messages = i18n.Register(i18n.Catalog{
	// 理由文の骨組み。対象・理由・対応の 3 段を、この順で出す。
	idReason: {
		EN: "❌ Blocked: {{.Target}}\n\nReason: {{.Why}}\n\nAction: {{.How}}",
		JA: "❌ ブロック: {{.Target}}\n\n理由: {{.Why}}\n\n対応: {{.How}}",
	},
	idTarget: {
		EN: "hand edit of generated files: {{.Files}}",
		JA: "生成ファイルの手編集: {{.Files}}",
	},
	idWhy: {
		EN: "These files carry a generated-file marker ({{.Markers}}). The generator overwrites hand edits the next " +
			"time it runs, and the file stops matching its source.",
		JA: "これらのファイルには生成ファイルのマーカー（{{.Markers}}）がある。手で編集しても次に生成したときに上書きされ、" +
			"生成元とも食い違う。",
	},
	idHowCommand: {
		EN: "Change the generator's source instead, then regenerate the files by running {{.Commands}}.",
		JA: "代わりに生成元を直し、{{.Commands}} を実行して作り直す。",
	},
	idHowGenerator: {
		EN: "Change the generator's source instead, then regenerate the files with the generator named in the marker.",
		JA: "代わりに生成元を直し、マーカーに書かれた生成器で作り直す。",
	},
	idHowFallback: {
		EN: "If you cannot run the generator in this environment, do not write the files by hand: continue with the " +
			"rest of the task, and in your final report state the changes the generated files needed (file, keys or " +
			"symbols, and content). Do not write them by any other route either, such as a script, a path built in a " +
			"variable, or a different tool.",
		JA: "この環境で生成コマンドを実行できないときは、手で書かずに残りの作業を続け、最終報告に生成ファイルに必要だった変更" +
			"（ファイル・キーやシンボル・内容）を書く。スクリプト、変数で組み立てたパス、別のツールなど、別の手段でも書き込まない。",
	},
	idHowDeclare: {
		EN: "Only if the user explicitly told you in this conversation to edit these generated files, run " +
			"`{{.Declare}}` once, then retry the edit. Do not run it otherwise.",
		JA: "ユーザーがこの会話の中で生成ファイルの編集を明示的に指示した場合に限り、`{{.Declare}}` を 1 回実行してから" +
			"編集をやり直す。それ以外では実行しない。",
	},
	idDeclarePlaceholder: {
		EN: "<summary of the user's instruction>",
		JA: "<ユーザーの指示の要旨>",
	},
	idContextDeclared: {
		EN: "generated-edit-guard recorded the declaration for this session, so hand edits of the declared generated " +
			"files are allowed.",
		JA: "generated-edit-guard がこのセッションの宣言を記録した。宣言した生成ファイルの手編集を通す。",
	},
	idContextAllowed: {
		EN: "generated-edit-guard allowed this hand edit of generated files because the user's instruction was " +
			"declared in this session.",
		JA: "generated-edit-guard は、このセッションで宣言したユーザーの指示に基づき、生成ファイルの手編集を通した。",
	},
	idContextReport: {
		EN: "Your final report must include all of the following:\n" +
			"- The generated files you edited by hand: {{.Files}}\n" +
			"- Why you edited them, and the user's instruction behind it: {{.Reasons}}\n" +
			"- That the files must be regenerated with {{.Commands}}. Regenerating overwrites the hand edits unless " +
			"the generator's source carries the same change.",
		JA: "最終報告には次をすべて書くこと。\n" +
			"- 手で編集した生成ファイル: {{.Files}}\n" +
			"- 編集した理由とユーザーの指示: {{.Reasons}}\n" +
			"- 生成コマンド（{{.Commands}}）で作り直す必要があること。生成元に同じ変更を入れない限り、作り直すと手編集は上書きされる。",
	},
	idContextUnrecorded: {
		EN: "generated-edit-guard did not record these declared paths because their values are not fixed in the " +
			"command (variables or command substitution): {{.Paths}}. Declare them again with literal paths.",
		JA: "generated-edit-guard は、コマンドの中で値が決まらない（変数やコマンド置換を含む）ため、次の宣言のパスを記録しなかった: " +
			"{{.Paths}}。パスをそのまま書いて宣言し直す。",
	},
	idUnknownCommand: {
		EN: "the generator named in the file's marker",
		JA: "マーカーに書かれた生成器",
	},
	idMore: {EN: "{{.Count}} more", JA: "ほか {{.Count}} 件"},
})

const (
	// maxListed は一覧に出す項目の数である。超えた分は件数だけにする。
	maxListed = 5
	// maxPathRunes・maxReasonRunes・maxMarkerRunes・maxCommandRunes は、表示するパス・宣言の理由・マーカーの行・
	// 生成コマンドの長さの上限である。注入の文面を Codex の additionalContextLimit に収めるために切り詰める。
	maxPathRunes    = 200
	maxReasonRunes  = 300
	maxMarkerRunes  = 120
	maxCommandRunes = 200
)

// truncate は text を limit 文字（rune）までに切り詰める。パスは末尾が大事なので、keepTail なら先頭を落とす。
func truncate(text string, limit int, keepTail bool) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if keepTail {
		return "…" + string(runes[len(runes)-limit+1:])
	}
	return string(runes[:limit-1]) + "…"
}

// listed は items を limit 件までに切り詰めて ", " で連結し、残りを件数で足す。
func listed(language i18n.Language, items []string, limit int) string {
	if len(items) <= limit {
		return strings.Join(items, ", ")
	}
	more := messages.Text(language, idMore, map[string]any{"Count": len(items) - limit})
	return strings.Join(items[:limit], ", ") + ", " + more
}

func unique(items []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}

// denyReason は blocked の生成ファイルの手編集を止める理由文を組み立てる。
func denyReason(language i18n.Language, blocked []generated, cwd string) string {
	var files, markers, commands, quoted []string
	for _, file := range blocked {
		files = append(files, truncate(displayPath(file.path, cwd), maxPathRunes, true))
		markers = append(markers, py.QuoteJSON(truncate(file.marker, maxMarkerRunes, false)))
		if file.command != "" {
			commands = append(commands, "`"+truncate(file.command, maxCommandRunes, false)+"`")
		}
		quoted = append(quoted, py.ShlexQuote(file.path))
	}
	how := messages.T(language, idHowGenerator)
	if commands = unique(commands); len(commands) > 0 {
		how = messages.Text(language, idHowCommand, map[string]any{"Commands": strings.Join(commands, ", ")})
	}
	declare := "hhx " + DeclareCommand + " --reason \"" +
		messages.T(language, idDeclarePlaceholder) + "\" " + strings.Join(unique(quoted), " ")
	how += " " + messages.T(language, idHowFallback) + " " +
		messages.Text(language, idHowDeclare, map[string]any{"Declare": declare})
	return messages.Text(language, idReason, map[string]any{
		"Target": messages.Text(language, idTarget, map[string]any{"Files": listed(language, unique(files), maxListed)}),
		"Why":    messages.Text(language, idWhy, map[string]any{"Markers": listed(language, unique(markers), maxListed)}),
		"How":    how,
	})
}

// unrecordedNotice は記録できなかった宣言のパスを伝える文面を組み立てる。
func unrecordedNotice(language i18n.Language, paths []string) string {
	var shown []string
	for _, path := range unique(paths) {
		shown = append(shown, truncate(path, maxPathRunes, true))
	}
	return messages.Text(language, idContextUnrecorded, map[string]any{"Paths": listed(language, shown, maxListed)})
}

// reportItem は最終報告に載せる生成ファイル 1 つである。
type reportItem struct {
	path, reason, command string
}

// reportContext は宣言を受け付けたとき（notes）と、宣言で編集を通したとき（allowed）に注入する文面を組み立てる。
// Codex の additionalContextLimit に収まるまで、一覧に出す件数を減らす。
func reportContext(language i18n.Language, notes, allowed []reportItem, cwd string) string {
	text := ""
	for limit := maxListed; limit >= 1; limit-- {
		if text = buildReport(language, notes, allowed, cwd, limit); len(text) <= contextLimit {
			break
		}
	}
	return text
}

func buildReport(language i18n.Language, notes, allowed []reportItem, cwd string, limit int) string {
	var lines []string
	if len(notes) > 0 {
		lines = append(lines, messages.T(language, idContextDeclared))
	}
	if len(allowed) > 0 {
		lines = append(lines, messages.T(language, idContextAllowed))
	}
	var files, reasons, commands []string
	for _, item := range append(append([]reportItem{}, notes...), allowed...) {
		files = append(files, truncate(displayPath(item.path, cwd), maxPathRunes, true))
		reasons = append(reasons, py.QuoteJSON(truncate(item.reason, maxReasonRunes, false)))
		if item.command != "" {
			commands = append(commands, "`"+truncate(item.command, maxCommandRunes, false)+"`")
		}
	}
	commandText := messages.T(language, idUnknownCommand)
	if commands = unique(commands); len(commands) > 0 {
		commandText = listed(language, commands, limit)
	}
	lines = append(lines, messages.Text(language, idContextReport, map[string]any{
		"Files":    listed(language, unique(files), limit),
		"Reasons":  listed(language, unique(reasons), limit),
		"Commands": commandText,
	}))
	return strings.Join(lines, "\n")
}
