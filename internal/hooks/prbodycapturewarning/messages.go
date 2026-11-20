package prbodycapturewarning

import "github.com/HappyOnigiri/hhx/internal/i18n"

const (
	idMixedStreams = "pr-body-capture-warning.mixed-streams"
	idREPLCapture  = "pr-body-capture-warning.repl-capture"
	idAction       = "pr-body-capture-warning.action"
)

var messages = i18n.Register(i18n.Catalog{
	// stderr を本文と同じ出力先に向けると案内やログが混ざることを伝える。
	idMixedStreams: {
		EN: "PR body warning: This command sends stderr to the same destination as the PR body. " +
			"GitHub CLI hints or other diagnostic output may be mixed into the body.",
		JA: "PR 本文の警告。このコマンドは stderr と PR 本文を同じ出力先に送るため、GitHub CLI の案内や診断ログが本文に混ざることがあります。",
	},
	// REPL の .text() で受け取った文字列に stderr が混ざることを伝える。
	idREPLCapture: {
		EN: "PR body warning: REPL .text() can include stderr from the shell or GitHub CLI along with the PR body.",
		JA: "PR 本文の警告。REPL の .text() で取得すると、シェルや GitHub CLI の stderr が PR 本文に混ざることがあります。",
	},
	// 送信用の本文を標準出力からファイルに取り直すよう促す。
	idAction: {
		EN: "Before using this output in a PR update, run `gh pr view <PR> --json body --jq .body > file.md` " +
			"and read the file or pass it to `--body-file`.",
		JA: "PR の更新に使う場合は `gh pr view <PR> --json body --jq .body > file.md` で本文を取得し直し、ファイルを読むか `--body-file` に渡してください。",
	},
})
