package permissionrequestguard

import "github.com/HappyOnigiri/hhx/internal/i18n"

// 理由文は ask の原因が分からないまま出すので、具体的な書き換えは示さない。
// 対象を明示して狭める書き換えは確認なしで進めてよいことと、同じ操作を別のコマンド・言語・スクリプトファイルで
// 行う迂回の禁止を書き分ける（実測で、迂回を禁じるだけだと正当な書き換えまで止めてユーザーに尋ね、
// 書き換えを許すだけだと find -delete のような別のコマンドへ乗り換えた）。
const (
	idReason      = "permission-request-guard.reason"
	idTarget      = "permission-request-guard.target"
	idWhyBypass   = "permission-request-guard.why.bypass"
	idWhyApproval = "permission-request-guard.why.approval"
	idHow         = "permission-request-guard.how"
)

var messages = i18n.Register(i18n.Catalog{
	// 理由文の骨組み。対象・理由・対応の 3 段を、この順で出す。
	idReason: {
		EN: "❌ Blocked: {{.Target}}\n\nReason: {{.Why}}\n\nAction: {{.How}}",
		JA: "❌ ブロック: {{.Target}}\n\n理由: {{.Why}}\n\n対応: {{.How}}",
	},
	// 止めた対象。コマンドの中身ではなく「承認が要ると判定された」ことを示す。
	idTarget: {
		EN: "a Bash command that needs the user's approval",
		JA: "ユーザーの承認が必要と判定された Bash コマンド",
	},
	// bypassPermissions で ask になるのは主に本体の安全確認なので、その例を挙げる。利用者の ask のルールでも来る（実測）ので断定しない。
	idWhyBypass: {
		EN: "This command needs the user's approval even in bypass mode, usually because Claude Code's safety check " +
			"could not confirm that it is safe (for example, deleting through a variable that may be empty, deleting " +
			"the working directory, its ancestors, or a critical path, or syntax its parser cannot read). " +
			"It was denied automatically instead of waiting for the user.",
		JA: "このコマンドは bypass モードでもユーザーの承認が必要です。多くは Claude Code の安全確認で安全だと確かめられなかったためです" +
			" (例: 空になりうる変数を通した削除、作業ディレクトリ・その祖先・重要なパスの削除、パーサーが読めない構文)。" +
			"承認を待たずに自動で拒否しました。",
	},
	// bypass 以外のモードでは、未承認のコマンドも ask になる。承認を待たずに拒否したことを伝える。
	idWhyApproval: {
		EN: "The permission settings do not allow this command without the user's approval, " +
			"so it was denied automatically instead of waiting for the user.",
		JA: "権限の設定上、このコマンドはユーザーの承認なしに実行できないため、承認を待たずに自動で拒否しました。",
	},
	// 対象を明示して狭める書き換えは確認なしで進めさせ、同じ操作を形を変えて行う迂回を禁じる。無理ならユーザーへの報告に誘導する。
	idHow: {
		EN: "If you can reach the goal with a command whose targets are explicit and narrower " +
			"(for example, literal paths such as rm -rf build/tmp instead of `.`, a variable, or a path that depends on cd), " +
			"rewrite it and continue; you do not need to ask the user. " +
			"Do not perform the same operation in another form to get around this block, such as a different command " +
			"(for example, find -delete instead of rm), another language, or a script file you write and run. " +
			"If no such rewrite works, stop and tell the user what you wanted to run and why.",
		JA: "対象を明示して範囲を狭めたコマンドで目的を達成できるなら (例: `.`・変数・cd に頼るパスの代わりに rm -rf build/tmp のような" +
			"リテラルのパスを書く)、書き換えて進めてください。ユーザーへの確認は不要です。" +
			"同じ操作を、別のコマンド (例: rm の代わりに find -delete)・別の言語・スクリプトファイルに書いての実行など" +
			"形を変えて行い、このブロックを避けることはしないでください。" +
			"そうした書き換えができないなら、作業を止めて、実行したかった内容と目的をユーザーに伝えてください。",
	},
})

// reason は permission_mode に応じた理由文を組み立てる。
func reason(language i18n.Language, mode string) string {
	why := idWhyApproval
	if mode == bypassMode {
		why = idWhyBypass
	}
	return messages.Text(language, idReason, map[string]any{
		"Target": messages.T(language, idTarget),
		"Why":    messages.T(language, why),
		"How":    messages.T(language, idHow),
	})
}
