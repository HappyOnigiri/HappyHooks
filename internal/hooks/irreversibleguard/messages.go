package irreversibleguard

import "github.com/HappyOnigiri/hhx/internal/i18n"

// 理由文は「何を・なぜ・どうする」の 3 点を返す。回避を思いとどまらせるのはこの文面だけなので、
// 対応には、操作を使わずに目的を達成する書き換えは確認なしで進めてよいことと、
// 同じ操作を形を変えて実行する迂回（分かりにくい書き方・別ツール・スクリプトファイル経由）を禁じることを分けて書く。
// 発火したルールの対象の表記（target）は「❌ ブロック:」の後ろに出し、テストはこれでどのルールが発火したかを見る。
const (
	idReason = "irreversible-guard.reason"
	idHow    = "irreversible-guard.how"

	idWhyCred    = "irreversible-guard.why.credential"
	idWhyRemote  = "irreversible-guard.why.remote"
	idWhyPublish = "irreversible-guard.why.publish"
	idWhyGit     = "irreversible-guard.why.git"
	idWhyMachine = "irreversible-guard.why.machine"
	idWhySecret  = "irreversible-guard.why.secret"
	idWhyGuard   = "irreversible-guard.why.guard"
)

// 対象の表記の ID。A〜G 類の規則ごとに 1 つある。
const (
	idRevocationAPI = "irreversible-guard.target.revocation-api"
	idRevokeHTTP    = "irreversible-guard.target.revoke-http"
	idGHAPIRevoke   = "irreversible-guard.target.gh-api-revoke"
	idGHLogout      = "irreversible-guard.target.gh-auth-logout"
	idGHSecret      = "irreversible-guard.target.gh-secret"
	idGHDeployKey   = "irreversible-guard.target.gh-deploy-key"
	idKeychain      = "irreversible-guard.target.keychain"
	idGcloudRevoke  = "irreversible-guard.target.gcloud-revoke"
	idNPMToken      = "irreversible-guard.target.npm-token-revoke"
	idGPGSecret     = "irreversible-guard.target.gpg-secret-key"
	idOPItem        = "irreversible-guard.target.1password-item"
	idAWSSecret     = "irreversible-guard.target.aws-secret"

	idNPMPublish   = "irreversible-guard.target.npm-publish"
	idYarnPublish  = "irreversible-guard.target.yarn-publish"
	idGemPublish   = "irreversible-guard.target.gem-publish"
	idTwinePublish = "irreversible-guard.target.twine-upload"
	idCargoPublish = "irreversible-guard.target.cargo-publish"

	idGHDelete       = "irreversible-guard.target.gh-delete"
	idGHAPIDelete    = "irreversible-guard.target.gh-api-delete"
	idGcloudDelete   = "irreversible-guard.target.gcloud-delete"
	idAWSDelete      = "irreversible-guard.target.aws-delete"
	idAWSS3          = "irreversible-guard.target.aws-s3-delete"
	idTFDestroy      = "irreversible-guard.target.terraform-destroy"
	idTFApplyDestroy = "irreversible-guard.target.terraform-apply-destroy"
	idMysqladmin     = "irreversible-guard.target.mysqladmin-drop"
	idHTTPDelete     = "irreversible-guard.target.http-delete"
	idDB             = "irreversible-guard.target.database"

	idReflog = "irreversible-guard.target.git-reflog-expire"
	idGC     = "irreversible-guard.target.git-gc-prune"
	idStash  = "irreversible-guard.target.git-stash-clear"

	idDiskutil = "irreversible-guard.target.diskutil-erase"
	idAPFS     = "irreversible-guard.target.diskutil-apfs-delete"
	idTmutil   = "irreversible-guard.target.tmutil-delete"

	idSecretRM       = "irreversible-guard.target.secret-delete"
	idSecretInPlace  = "irreversible-guard.target.secret-in-place"
	idSecretMV       = "irreversible-guard.target.secret-move"
	idSecretRedirect = "irreversible-guard.target.secret-redirect"
	idSecretTee      = "irreversible-guard.target.secret-tee"
	idSecretEdit     = "irreversible-guard.target.secret-edit"
	idSecretPatch    = "irreversible-guard.target.secret-patch"

	idGuard = "irreversible-guard.target.guard"
)

var messages = i18n.Register(i18n.Catalog{
	// 理由文の骨組み。対象・理由・対応の 3 段を、この順で出す。
	idReason: {
		EN: "❌ Blocked: {{.Target}}\n\nReason: {{.Why}}\n\nAction: {{.How}}",
		JA: "❌ ブロック: {{.Target}}\n\n理由: {{.Why}}\n\n対応: {{.How}}",
	},
	// 操作を使わない書き換えは確認なしで進めさせ、同じ操作を形を変えて実行する迂回を禁じる。
	// 判定はコマンドの文字列を区切りごとに見るので、別の対象への操作が同居しただけで止まることがある。その場合の書き換えも許す。
	// 操作そのものが必要ならユーザーへの報告に誘導する。承認されても実行はユーザーが行う。
	idHow: {
		EN: "If you can reach the goal without this operation, rewrite the command and continue " +
			"(for example, drop a step you do not need); you do not need to ask the user. " +
			"If the command does not actually perform the blocked operation on that target " +
			"(for example, it only deletes a temporary file), you may also split it into separate commands. " +
			"Do not perform the same operation in another form to get around this block, such as an obfuscated command, " +
			"a different tool, or a script file you write and run. " +
			"If the operation itself is needed, tell the user the operation, the target, and the purpose, " +
			"and ask for instructions. Even if the user approves, the user normally runs it themselves.",
		JA: "この操作をせずに目的を達成できるなら、コマンドを書き換えて進めてください " +
			"(例: 不要な手順を外す)。ユーザーへの確認は不要です。" +
			"ブロックされた対象にその操作を実際には行っていない (例: 消すのは一時ファイルだけ) なら、" +
			"コマンドを分けて実行してもかまいません。" +
			"同じ操作を、分かりにくいコマンドへの書き換え・別ツール・スクリプトファイルに書いての実行など" +
			"形を変えて行い、このブロックを避けることはしないでください。" +
			"この操作そのものが必要なら、実行したい操作・対象・目的をユーザーに伝えて指示を仰いでください。" +
			"承認された場合も、原則ユーザー自身が実行します。",
	},

	// A 類: 資格情報。どのシステムが依存しているかはユーザーにしか分からない。
	idWhyCred: {
		EN: "Revoking or deleting a credential cannot be undone. Only the user knows which systems depend on it, " +
			"so the user decides whether to run it.",
		JA: "資格情報の失効・削除は元に戻せません。その資格情報にどのシステムが依存しているかは" +
			"ユーザーにしか分からないため、実行の可否はユーザーが判断します。",
	},
	// C 類: リモートリソースの恒久削除。
	idWhyRemote: {
		EN: "This permanently deletes a remote resource; it cannot be restored from history or a trash.",
		JA: "リモートリソースの恒久削除で、履歴やゴミ箱からの復元ができません。",
	},
	// B 類: 公開・取り下げは即座に伝播し、取り消せない。
	idWhyPublish: {
		EN: "Publishing or withdrawing a package reaches the world at once and cannot be taken back " +
			"(some registries also forbid ever republishing under the same name).",
		JA: "外部への公開・取り下げは即座に世界へ伝播し、取り消せません" +
			" (レジストリによっては同名での再公開も恒久にできません)。",
	},
	// D 類: 到達不能オブジェクトの物理削除。
	idWhyGit: {
		EN: "This physically deletes unreachable objects, taking away any chance of recovery through reflog or fsck.",
		JA: "到達不能オブジェクトの物理削除で、reflog や fsck による復元の余地ごと失われます。",
	},
	// E 類: マシン上の消去。
	idWhyMachine: {
		EN: "This irreversibly erases data or backups on the machine.",
		JA: "マシン上のデータ・バックアップの不可逆な消去です。",
	},
	// F 類: 秘密ファイル。新規作成も含めてユーザーが書く。
	idWhySecret: {
		EN: "The agent cannot judge the impact of creating, editing, or deleting secrets and keys outside Git by mistake. " +
			"The user writes secret files directly, including creating new ones.",
		JA: "Git 管理外の秘密情報・鍵は、誤った作成・編集・削除による影響をエージェント側で" +
			"判断できません。秘密ファイルへの直接書き込みは、新規作成も含めユーザーが行います。",
	},
	// G 類: エージェントの安全ガード自体の削除・移動。
	idWhyGuard: {
		EN: "This deletes or moves the agent's safety guards (hooks and their config files). " +
			"The user changes how the guards are set up.",
		JA: "エージェントの安全ガード (hooks・設定ファイル) の削除・移動です。" +
			"ガードの構成変更はユーザーが行います。",
	},

	// --- A. 資格情報の失効・削除 ---
	idRevocationAPI: {EN: "GitHub Credential Revocation API", JA: "GitHub Credential Revocation API"},
	idRevokeHTTP: {
		EN: "an HTTP request to a revoke endpoint",
		JA: "失効 (revoke) エンドポイントへの HTTP リクエスト",
	},
	idGHAPIRevoke: {EN: "a revoke endpoint of gh api", JA: "gh api の失効 (revoke) エンドポイント"},
	idGHLogout:    {EN: "gh auth logout", JA: "gh auth logout"},
	idGHSecret: {
		EN: "deleting a secret or key on GitHub (its value cannot be read back)",
		JA: "GitHub 上の Secret・鍵の削除 (値は読み返せない)",
	},
	idGHDeployKey:  {EN: "deleting a deploy key on GitHub", JA: "GitHub 上の Deploy Key の削除"},
	idKeychain:     {EN: "deleting from the Keychain (security delete-*)", JA: "Keychain の削除 (security delete-*)"},
	idGcloudRevoke: {EN: "revoking gcloud credentials", JA: "gcloud の認証失効"},
	idNPMToken:     {EN: "npm token revoke", JA: "npm token revoke"},
	idGPGSecret:    {EN: "deleting a GPG secret key", JA: "GPG 秘密鍵の削除"},
	idOPItem:       {EN: "deleting a 1Password item", JA: "1Password アイテムの削除"},
	idAWSSecret:    {EN: "immediate permanent deletion in AWS Secrets Manager", JA: "AWS Secrets Manager の即時完全削除"},

	// --- B. 公開・取り下げ ---
	idNPMPublish:   {EN: "publishing to or withdrawing from the npm registry", JA: "npm レジストリへの公開・取り下げ"},
	idYarnPublish:  {EN: "publishing to the npm registry (yarn)", JA: "npm レジストリへの公開 (yarn)"},
	idGemPublish:   {EN: "publishing to or withdrawing from RubyGems", JA: "RubyGems への公開・取り下げ"},
	idTwinePublish: {EN: "publishing to PyPI (twine upload)", JA: "PyPI への公開 (twine upload)"},
	idCargoPublish: {EN: "publishing to or withdrawing from crates.io", JA: "crates.io への公開・取り下げ"},

	// --- C. リモートリソースの恒久削除 ---
	idGHDelete:       {EN: "permanent deletion with gh (repo/release/gist)", JA: "gh の恒久削除 (repo/release/gist)"},
	idGHAPIDelete:    {EN: "DELETE with gh api", JA: "gh api の DELETE"},
	idGcloudDelete:   {EN: "a gcloud delete operation", JA: "gcloud の削除操作"},
	idAWSDelete:      {EN: "an aws delete operation (delete-*)", JA: "aws の削除操作 (delete-*)"},
	idAWSS3:          {EN: "deleting aws s3 objects or buckets", JA: "aws s3 のオブジェクト・バケット削除"},
	idTFDestroy:      {EN: "terraform destroy", JA: "terraform destroy"},
	idTFApplyDestroy: {EN: "terraform apply -destroy", JA: "terraform apply -destroy"},
	idMysqladmin:     {EN: "mysqladmin drop", JA: "mysqladmin drop"},
	idHTTPDelete:     {EN: "DELETE on a cloud API via an HTTP client", JA: "HTTP クライアントによるクラウド API の DELETE"},
	idDB:             {EN: "a destructive database operation (DROP/TRUNCATE)", JA: "データベースの破壊操作 (DROP/TRUNCATE)"},

	// --- D. Git オブジェクトの物理破壊 ---
	idReflog: {EN: "git reflog expire", JA: "git reflog expire"},
	idGC:     {EN: "git gc --prune=now", JA: "git gc --prune=now"},
	idStash:  {EN: "git stash clear", JA: "git stash clear"},

	// --- E. マシン上の不可逆消去 ---
	idDiskutil: {EN: "erasing a disk with diskutil", JA: "diskutil によるディスク消去"},
	idAPFS:     {EN: "diskutil apfs delete*", JA: "diskutil apfs delete*"},
	idTmutil:   {EN: "deleting a Time Machine backup", JA: "Time Machine バックアップの削除"},

	// --- F. 秘密ファイル ---
	idSecretRM:       {EN: "deleting or destroying a secret file", JA: "秘密ファイルの削除・破壊"},
	idSecretInPlace:  {EN: "rewriting a secret file in place", JA: "秘密ファイルの in-place 書き換え"},
	idSecretMV:       {EN: "moving a secret file", JA: "秘密ファイルの移動"},
	idSecretRedirect: {EN: "overwriting a secret file with a redirect", JA: "秘密ファイルの上書きリダイレクト"},
	idSecretTee:      {EN: "writing to a secret file with tee", JA: "秘密ファイルへの tee 書き込み"},
	idSecretEdit:     {EN: "editing a secret file directly", JA: "秘密ファイルへの直接編集"},
	idSecretPatch:    {EN: "applying a patch to a secret file", JA: "秘密ファイルへのパッチ適用"},

	// --- G. エージェントのガード ---
	idGuard: {EN: "deleting or moving an agent guard", JA: "エージェントガードの削除・移動"},
})

// target は発火したルールの対象である。token があれば、発火したトークン（パス）を括弧で添える。
type target struct {
	id    string
	token string
}

func (t target) text(language i18n.Language) string {
	text := messages.T(language, t.id)
	if t.token != "" {
		text += " (" + t.token + ")"
	}
	return text
}

func reason(language i18n.Language, found target, why string) string {
	return messages.Text(language, idReason, map[string]any{
		"Target": found.text(language),
		"Why":    messages.T(language, why),
		"How":    messages.T(language, idHow),
	})
}
