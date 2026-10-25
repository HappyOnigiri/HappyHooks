package discardguard

import (
	"strings"

	"github.com/HappyOnigiri/hhx/internal/i18n"
)

// 理由文には「ガードが働く形への書き直し」だけを書く。cp 退避のような迂回路を案内すると、snapshot なしの破棄へ誘導することになる。
// Claude Code と Codex の両方に出るので、片方にしか無いツール名を書かない。
const (
	idUnresolved = "discard-guard.unresolved"
	idFailed     = "discard-guard.failed"
)

// ruleLabelID は discardRules の ID から、理由文に出すラベルのカタログの ID を作る。
func ruleLabelID(rule string) string {
	return "discard-guard.rule." + rule
}

var messages = i18n.Register(i18n.Catalog{
	// 対象のリポジトリを静的に特定できないとき。追えない形を挙げ、スナップショットを作れる書き直しだけを案内する。
	// cp 退避や別コマンドでの迂回は代替にならないと明記し、書き直せなければユーザーに指示を仰がせる。
	idUnresolved: {
		EN: `❌ Blocked: {{.Label}}

Reason: This operation discards uncommitted changes, but which repository it targets
        cannot be determined statically from cd or git -C, so no restore snapshot can
        be taken. The hook cannot follow globs, paths that do not exist when the hook
        runs, pushd / popd, sh -c, here-documents fed to a shell, overrides via
        GIT_DIR / --git-dir / --work-tree, and variables other than those assigned a
        value once, unconditionally, earlier in the same command.

Action: Rewrite it as follows, and it will pass after a snapshot is taken.
        1. Give the cd / git -C paths as literals, or as variables assigned a literal
           once earlier in the same command (D=/abs/path; git -C $D ...), not inside
           if / for and not after && / || (several cd are fine)
        2. Do not use sh -c or pushd; run the discarding command as its own Bash call
        If you only want to write it as text without running it, use a file editing tool.
        A cp backup or a different command to get around this block takes no snapshot,
        so it is not an alternative.
        If you cannot rewrite it, tell the user the operation, the target, and the purpose,
        and ask for instructions.`,
		JA: `❌ ブロック: {{.Label}}

理由: 未コミットの変更を破棄する操作ですが、cd や git -C でどのリポジトリが対象か
      静的に特定できず、復元用スナップショットを作れません。追えないのは、glob、
      フック実行時に存在しないパス、pushd / popd、sh -c 経由、シェルへ流し込む
      ヒアドキュメント、GIT_DIR / --git-dir / --work-tree による差し替えと、
      同じコマンドの前の方で条件なしに 1 回だけ代入した変数以外の変数です。

対応: 次のように書き直せば、スナップショットを作ってから通ります。
      1. cd / git -C のパスをリテラルか、同じコマンドの前の方でリテラルを 1 回だけ
         代入した変数で書く (D=/abs/path; git -C $D ...)。代入は if / for の中や
         && / || の後に置かない (cd は複数回あってよい)
      2. sh -c や pushd を使わず、破棄コマンドを単独の Bash 呼び出しにする
      実行せず文字列として書きたいだけなら、ファイルを編集するツールを使ってください。
      cp 退避や別コマンドでの迂回はスナップショットを作らないので代替になりません。
      書き直せない場合は、操作・対象・目的をユーザーに伝えて指示を仰いでください。`,
	},
	// スナップショットの作成に失敗したとき。書き直しでは通らないと伝え、一時的な失敗のときだけ 1 度の再実行を許す。
	// 作業ツリーを勝手に片付けさせない。
	idFailed: {
		EN: `❌ Blocked: {{.Label}}

Reason: This operation discards uncommitted changes, but taking the restore snapshot
        failed (not under git, a git command failed, it timed out, and so on).

Action: Rewriting the command will not make it pass. Only if you suspect a temporary
        failure such as an index.lock conflict, run the same command once more; otherwise
        do not work around this block, and tell the user the operation, the target, and
        the purpose, and ask for instructions. Even if a dirty working tree keeps you from
        continuing, do not clean it up on your own.`,
		JA: `❌ ブロック: {{.Label}}

理由: 未コミットの変更を破棄する操作ですが、復元用スナップショットの作成に失敗しました
      (git 管理下でない、git コマンドが失敗した、タイムアウトしたなど)。

対応: 書き直しでは通りません。index.lock の競合のような一時的な失敗が疑われる場合だけ
      同じコマンドを 1 度再実行し、それ以外は迂回せず、操作・対象・目的をユーザーに伝えて
      指示を仰いでください。作業ツリーが汚れて進められない場合も勝手に片付けないこと。`,
	},
	// 以下は discardRules の各ルールのラベル。理由文の「❌ ブロック:」の後ろに " / " で連結して出す。
	ruleLabelID(ruleResetHard): {EN: "git reset --hard", JA: "git reset --hard"},
	ruleLabelID(ruleCheckout):  {EN: "git checkout (discarding form)", JA: "git checkout (破棄形)"},
	ruleLabelID(ruleSwitch):    {EN: "git switch (forced switch)", JA: "git switch (強制切り替え)"},
	ruleLabelID(ruleRestore):   {EN: "git restore (working tree)", JA: "git restore (作業ツリー)"},
	ruleLabelID(ruleClean):     {EN: "git clean -f", JA: "git clean -f"},
	ruleLabelID(ruleApply):     {EN: "git apply (discarding form)", JA: "git apply (破棄形)"},
})

// ruleLabels は発火したルールのラベルを、rules の順に " / " で連結する。
func ruleLabels(language i18n.Language, rules []string) string {
	labels := make([]string, len(rules))
	for index, rule := range rules {
		labels[index] = messages.T(language, ruleLabelID(rule))
	}
	return strings.Join(labels, " / ")
}

func denyUnresolved(language i18n.Language, label string) string {
	return messages.Text(language, idUnresolved, map[string]any{"Label": label})
}

func denyFailed(language i18n.Language, label string) string {
	return messages.Text(language, idFailed, map[string]any{"Label": label})
}
