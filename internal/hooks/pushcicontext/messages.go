package pushcicontext

import "github.com/HappyOnigiri/hhx/internal/i18n"

// waitCommand は push・PR 作成の後に待つコマンドである。hhx に内蔵した wait-ci に固定する。
const waitCommand = "hhx wait-ci --progress"

const (
	idPostPush     = "push-ci-context.event.push.post"
	idPrePush      = "push-ci-context.event.push.pre"
	idPostDispatch = "push-ci-context.event.dispatch.post"
	idPreDispatch  = "push-ci-context.event.dispatch.pre"

	idGuidance         = "push-ci-context.guidance.wait-ci"
	idDispatchGuidance = "push-ci-context.guidance.dispatch"
	idOrder            = "push-ci-context.guidance.order"
	idBash             = "push-ci-context.wait.bash"
	idCodex            = "push-ci-context.wait.codex"
	idLabelWaitCI      = "push-ci-context.label.wait-ci"
	idLabelDispatch    = "push-ci-context.label.dispatch"

	idMultipleCandidates  = "push-ci-context.poll.multiple-candidates"
	idRegistrationTimeout = "push-ci-context.poll.registration-timeout"
)

var messages = i18n.Register(i18n.Catalog{
	// 案内の書き出し。PreToolUse は実行前なので「成功したら」と書く。
	idPostPush: {EN: "The push / PR creation succeeded. ", JA: "push / PR 作成が成功した。"},
	idPrePush:  {EN: "If this push / PR creation succeeds, do the following. ", JA: "この push / PR 作成が成功したら、"},
	idPostDispatch: {
		EN: "The workflow dispatch succeeded. ",
		JA: "workflow dispatch が成功した。",
	},
	idPreDispatch: {
		EN: "If this workflow dispatch succeeds, do the following. ",
		JA: "この workflow dispatch が成功したら、",
	},
	// push・PR 作成の後は wait-ci で CI の完了まで待たせる。終了コードを失わないこと、失敗は直して追うことも伝える。
	// detached HEAD や別名のブランチへの push でも PR を探せるので、引数を足したり別の手段で待ったりさせない。
	idGuidance: {
		EN: "Run `{{.Command}}` with no arguments and wait until it finishes in this turn. This also works on a " +
			"detached HEAD and after pushing to a branch whose name differs from the local one, because it finds the PR " +
			"from the HEAD commit; do not wait some other way. pending / settling means " +
			"not finished. Do not lose the exit code through a pipe; for exit 0, tell success from a skip by the verdict " +
			"line. On failure, fix the cause, push again, and follow it until CI completes.\n{{.Order}}",
		JA: "`{{.Command}}` を引数なしで実行し、このターンで終了まで待つ。detached HEAD でも、ローカルと違う名前のブランチへ " +
			"push した後でも、HEAD の commit から PR を探すのでそのまま使い、別の手段で待たない。pending / settling は未完了。" +
			"パイプで終了コードを失わず、exit 0 は成功とスキップを結論行で区別する。失敗は原因を直して再pushし、CI完了まで追跡する。\n" +
			"{{.Order}}",
	},
	// workflow dispatch の後は run を watch させる。run ID が無ければ登録確認と watch を 1 回の待機にまとめさせる。
	idDispatchGuidance: {
		EN: "The workflow dispatch was accepted. Using the run ID from the returned URL, wait for " +
			"`gh run watch <run-id> --exit-status` to finish in this turn. If the run ID is not available yet, run the " +
			"registration loop below and the watch inside a single long-wait tool call, and do not hand control back " +
			"to the model every 30 seconds.\n{{.Order}}",
		JA: "workflow dispatch の受付が成功した。返された URL の run ID を使い、" +
			"`gh run watch <run-id> --exit-status` をこのターンで終了まで待つ。run ID がまだ取得できない" +
			"場合は、下の登録確認ループと watch を同一の長時間待機ツール呼び出し内で実行し、" +
			"30秒ごとにモデルへ制御を戻さない。\n{{.Order}}",
	},
	// 待機を残作業の後に回させる。CI はモデルの待機と無関係に進むので、先に他の作業を片付けても完了は遅れない。
	// 逆に先に待つと、PR 本文の更新や別 PR の改修が待ち時間ぶん止まる。
	idOrder: {
		EN: "However, leave the wait for the end of this turn. If the user's request or other points in this context " +
			"(updating the PR body, fixing another PR, applying review comments, and so on) are not started yet, finish " +
			"them first, and start waiting only when nothing is left. CI progresses whether or not you wait, so doing " +
			"the work first does not delay its completion. Do not postpone other work because of the wait, and do not " +
			"report and end the turn before waiting.\n",
		JA: "ただし待機はこのターンの最後に回す。ユーザーの依頼・このコンテキストに入った他の指摘" +
			"（PR 本文の更新、別 PR の改修、レビュー指摘の反映など）で未着手のものがあれば先に片付け、" +
			"残りが無くなってから待機を始める。CI は待機の有無に関わらず進むため、先に作業しても完了は遅れない。" +
			"待機を理由に他の作業を後回しにしたり、待機前に報告してターンを終えたりしない。\n",
	},
	// Claude Code の Bash での待ち方。{{.Command}} は待つコマンドである。
	idBash: {
		EN: "In Bash, run the following with `timeout:600000`, and if it moves to the background, follow it through " +
			"its output file until it ends: `{{.Command}}`\n",
		JA: "Bash は次を `timeout:600000` で実行し、バックグラウンドへ移った場合も出力ファイルで終了まで追跡する: `{{.Command}}`\n",
	},
	// Codex の Code Mode での待ち方。{{.Label}} は待つものの呼び名、{{.Command}} は待つコマンドの JSON 文字列である。
	// 定期報告と長い待機の回避の例外にし、1 回の待機で終了まで追わせる。
	idCodex: {
		EN: "During {{.Label}}, make an exception to periodic reports and to avoiding waits longer than 60 seconds; " +
			"report only before it starts, when it ends, and when a decision is needed. In Code Mode, wait like this.\n" +
			codeModeStart + "\"exit code not confirmed\"" + codeModeEnd +
			"If the outer call returns a cell_id, wait on the same ID with functions.wait (yield_time_ms:3600000). " +
			"Do not restart it or run a second, overlapping poll; if functions.wait returns while the cell is still running, " +
			"wait on the same ID again until it ends. Without Code Mode, follow it until it ends with write_stdin on empty " +
			"input, using the longest wait time available.\n",
		JA: "{{.Label}}中は定期報告と60秒を超える待機回避の例外とし、開始前と終了・判断が必要な時だけ報告する。" +
			"Code Mode では次の形で待つ。\n" +
			codeModeStart + "\"終了コード未確認\"" + codeModeEnd +
			"外側が cell_id を返したら同じIDを functions.wait（yield_time_ms:3600000）で待つ。再起動・二重ポーリングはしない。" +
			"Code Mode がなければ空入力の write_stdin を利用可能な最大待機時間で終了まで追跡する。\n",
	},
	// Codex の案内の「〜中は」に入る呼び名。
	idLabelWaitCI:   {EN: "hhx wait-ci", JA: "hhx wait-ci "},
	idLabelDispatch: {EN: "the wait for the workflow run", JA: "workflow run の完了待機"},
	// 登録確認ループが echo する文面。シェルの単一引用符の中に入るので、' を含めない。
	idMultipleCandidates: {
		EN: "several workflow run candidates; cannot tell which one",
		JA: "workflow run の候補が複数あり特定できない",
	},
	idRegistrationTimeout: {
		EN: "timed out after 90 minutes waiting for the workflow run to register",
		JA: "workflow run の登録待ちが90分でタイムアウトした",
	},
})

// codeModeStart と codeModeEnd は Code Mode で待つコードである。間に終了コードが無いときのエラーの文面（JSON 文字列）が入る。
const (
	codeModeStart = "```javascript\n" +
		"// @exec: {\"yield_time_ms\":3600000}\n" +
		"let r = await tools.exec_command({cmd:{{.Command}},yield_time_ms:1000});\n" +
		"let output = r.output ?? \"\";\n" +
		"while (r.session_id != null) {\n" +
		"  r = await tools.write_stdin({session_id:r.session_id,chars:\"\",yield_time_ms:300000});\n" +
		"  output += r.output ?? \"\";\n" +
		"}\n" +
		"if (typeof r.exit_code !== \"number\") throw new Error("
	codeModeEnd = ");\n" +
		"text({exit_code:r.exit_code,output});\n" +
		"```\n"
)
