package blockingcommandguard

import "github.com/HappyOnigiri/hhx/internal/i18n"

const idReason = "blocking-command-guard.reason"

var messages = i18n.Register(i18n.Catalog{
	// 入力元の不足や対話・継続モードを説明し、時間上限や検査の迂回でなく目的に合う修正を促す。
	idReason: {
		EN: "{{.Label}}: rejected command: {{.Command}}\n" +
			"This command requests input without an explicit input source, interactive operation, " +
			"or an unbounded foreground mode. " +
			"Specify an input file, pipe, or heredoc; supply the required values; or use a non-interactive, finite mode. " +
			"Remove an unnecessary command. For intentional continuous work, explicitly run it in the background. " +
			"Do not add a timeout or automatically approve destructive operations just to avoid waiting. " +
			"Proceed with a corrected command that removes the wait without asking for confirmation. " +
			"Do not bypass this check by disguising the same operation with another tool or a script file.",
		JA: "{{.Label}}: 拒否対象コマンド: {{.Command}}\n" +
			"入力元のない読み取り、対話操作、または終了条件のない前景実行が含まれています。" +
			"入力ファイル・パイプ・heredoc や必要な値を明示するか、非対話で終了するモードを使ってください。" +
			"不要なコマンドは削除してください。意図した継続処理は、バックグラウンド実行を明示してください。" +
			"待機を避けるためだけにタイムアウトを追加したり、破壊的な操作を自動承認したりしないでください。" +
			"待機の原因を取り除く修正は、確認なしで進めてください。" +
			"同じ操作を別ツールやスクリプトファイルで隠して、この検査を迂回しないでください。",
	},
})
