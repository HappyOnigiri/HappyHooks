package irreversibleguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HappyOnigiri/hhx/internal/hooktest"
	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

// 理由文に出るラベル（どのルールが発火したかの判別用）。テストを流す言語でカタログから引く。
var (
	labelRevocationAPI  = messages.T(hooktest.Language, idRevocationAPI)
	labelRevokeHTTP     = messages.T(hooktest.Language, idRevokeHTTP)
	labelGHAPIRevoke    = messages.T(hooktest.Language, idGHAPIRevoke)
	labelGHLogout       = messages.T(hooktest.Language, idGHLogout)
	labelGHSecret       = messages.T(hooktest.Language, idGHSecret)
	labelGHDeployKey    = messages.T(hooktest.Language, idGHDeployKey)
	labelKeychain       = messages.T(hooktest.Language, idKeychain)
	labelGcloudRevoke   = messages.T(hooktest.Language, idGcloudRevoke)
	labelNPMToken       = messages.T(hooktest.Language, idNPMToken)
	labelGPGSecret      = messages.T(hooktest.Language, idGPGSecret)
	labelOPItem         = messages.T(hooktest.Language, idOPItem)
	labelAWSSecret      = messages.T(hooktest.Language, idAWSSecret)
	labelNPMPublish     = messages.T(hooktest.Language, idNPMPublish)
	labelYarnPublish    = messages.T(hooktest.Language, idYarnPublish)
	labelGemPublish     = messages.T(hooktest.Language, idGemPublish)
	labelTwinePublish   = messages.T(hooktest.Language, idTwinePublish)
	labelCargoPublish   = messages.T(hooktest.Language, idCargoPublish)
	labelGHDelete       = messages.T(hooktest.Language, idGHDelete)
	labelGHAPIDelete    = messages.T(hooktest.Language, idGHAPIDelete)
	labelGcloudDelete   = messages.T(hooktest.Language, idGcloudDelete)
	labelAWSDelete      = messages.T(hooktest.Language, idAWSDelete)
	labelAWSS3          = messages.T(hooktest.Language, idAWSS3)
	labelTFDestroy      = messages.T(hooktest.Language, idTFDestroy)
	labelTFApplyDestroy = messages.T(hooktest.Language, idTFApplyDestroy)
	labelMysqladmin     = messages.T(hooktest.Language, idMysqladmin)
	labelHTTPDelete     = messages.T(hooktest.Language, idHTTPDelete)
	labelDB             = messages.T(hooktest.Language, idDB)
	labelReflog         = messages.T(hooktest.Language, idReflog)
	labelGC             = messages.T(hooktest.Language, idGC)
	labelStash          = messages.T(hooktest.Language, idStash)
	labelDiskutil       = messages.T(hooktest.Language, idDiskutil)
	labelAPFS           = messages.T(hooktest.Language, idAPFS)
	labelTmutil         = messages.T(hooktest.Language, idTmutil)
	labelSecretRM       = messages.T(hooktest.Language, idSecretRM)
	labelSecretInPlace  = messages.T(hooktest.Language, idSecretInPlace)
	labelSecretMV       = messages.T(hooktest.Language, idSecretMV)
	labelSecretRedirect = messages.T(hooktest.Language, idSecretRedirect)
	labelSecretTee      = messages.T(hooktest.Language, idSecretTee)
	labelSecretEdit     = messages.T(hooktest.Language, idSecretEdit)
	labelSecretPatch    = messages.T(hooktest.Language, idSecretPatch)
	labelGuard          = messages.T(hooktest.Language, idGuard)
)

// fakeHome と fakeExecutable は G 類のテストに使う架空のホームディレクトリと hhx の実行ファイルである。
const (
	fakeHome       = "/Users/alice"
	fakeExecutable = fakeHome + "/.local/bin/hhx"
)

// ホームディレクトリと実行ファイルを架空の値に固定し、実行者の環境で G 類の判定が変わらないようにする。
func TestMain(m *testing.M) {
	_ = os.Setenv("HOME", fakeHome)
	executable = func() (string, error) { return fakeExecutable, nil }
	os.Exit(m.Run())
}

func check(t *testing.T, want string, cases []hooktest.Case) {
	t.Helper()
	hooktest.CheckTable(t, Definition(), want, "/tmp", cases)
}

// --- A. 資格情報の失効・削除 ---

func TestCredentialRevocationAPI(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "curl -sS -X POST -H 'Accept: application/vnd.github+json' " +
			"https://api.github.com/credentials/revoke -d '{\"credentials\":[\"x\"]}'", Label: labelRevocationAPI},
		{Command: "wget --post-data='{}' https://api.github.com/credentials/revoke", Label: labelRevocationAPI},
		{Command: "curl -X POST https://slack.com/api/auth.revoke", Label: labelRevokeHTTP},
		{Command: "curl -X POST https://oauth2.googleapis.com/revoke -d token=x", Label: labelRevokeHTTP},
		{Command: "gh api -X POST /credentials/revoke", Label: labelGHAPIRevoke},
	})
}

func TestCLICredentialRemoval(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "gh auth logout", Label: labelGHLogout},
		{Command: "gh auth logout --hostname github.com", Label: labelGHLogout},
		{Command: "gh secret delete GH_TOKEN", Label: labelGHSecret},
		{Command: "gh variable delete FOO -R o/r", Label: labelGHSecret},
		{Command: "gh ssh-key delete 123 --yes", Label: labelGHSecret},
		{Command: "gh gpg-key delete 1", Label: labelGHSecret},
		{Command: "gh repo deploy-key delete 1", Label: labelGHDeployKey},
		{Command: "security delete-generic-password -a alice -s gh-token", Label: labelKeychain},
		{Command: "security delete-keychain login.keychain", Label: labelKeychain},
		{Command: "security delete-internet-password -s example.com", Label: labelKeychain},
		{Command: "gcloud auth revoke", Label: labelGcloudRevoke},
		{Command: "gcloud auth application-default revoke", Label: labelGcloudRevoke},
		{Command: "npm token revoke abcd", Label: labelNPMToken},
		{Command: "gpg --batch --delete-secret-key ABCD", Label: labelGPGSecret},
		{Command: "gpg2 --delete-secret-key ABCD", Label: labelGPGSecret},
		{Command: "op item delete 'GitHub PAT'", Label: labelOPItem},
		{Command: "aws secretsmanager delete-secret --secret-id x --force-delete-without-recovery", Label: labelAWSSecret},
		{Command: "gcloud iam service-accounts keys delete abcdef --iam-account=x@y.iam", Label: labelGcloudDelete},
		{Command: "aws iam delete-access-key --access-key-id AKIAXXXX", Label: labelAWSDelete},
	})
}

// パス起動・区切り文字直結・rtk ラッパー・改行区切り・Unicode の空白。
func TestCredentialNotationVariants(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "/usr/local/bin/gh auth logout", Label: labelGHLogout},
		{Command: "./gh auth logout", Label: labelGHLogout},
		{Command: "/usr/bin/security delete-internet-password -s example.com", Label: labelKeychain},
		{Command: "/usr/local/bin/gcloud auth revoke", Label: labelGcloudRevoke},
		{Command: "true;gh auth logout", Label: labelGHLogout},
		{Command: "(gh auth logout)", Label: labelGHLogout},
		{Command: "false||gh auth logout", Label: labelGHLogout},
		{Command: "cd /tmp && gh ssh-key delete 1 --yes", Label: labelGHSecret},
		{Command: "rtk gh secret delete GH_TOKEN", Label: labelGHSecret},
		{Command: "echo start\ngh auth logout", Label: labelGHLogout},
		{Command: "gh  auth\tlogout", Label: labelGHLogout},
		{Command: "gh auth logout\n", Label: labelGHLogout},
		{Command: "gh　auth logout", Label: labelGHLogout},
		{Command: "gh auth logout　now", Label: labelGHLogout},
	})
}

// 語の一部にすぎない形（Python の \w は Unicode の文字も語の文字に含める）。
func TestCredentialLookalikes(t *testing.T) {
	check(t, "", hooktest.Commands(
		"gh auth logoutx",
		"gh auth logouté",
		"gh auth logout_all",
		"gh secret deleted",
		"gcloud auth unrevoke",
		"gcloud auth revoker",
	))
}

// --- B. レジストリへの公開・取り下げ ---

func TestRegistryPublish(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "npm publish", Label: labelNPMPublish},
		{Command: "npm publish --tag next", Label: labelNPMPublish},
		{Command: "npm publish --otp=123456", Label: labelNPMPublish},
		{Command: "pnpm publish --access public", Label: labelNPMPublish},
		{Command: "npm unpublish pkg@1.0.0", Label: labelNPMPublish},
		{Command: "npm deprecate pkg@1.0.0 'msg'", Label: labelNPMPublish},
		{Command: "yarn npm publish", Label: labelNPMPublish}, // npm ルールが先に当たる（判定は同じ）
		{Command: "yarn publish", Label: labelYarnPublish},
		{Command: "gem push pkg-1.0.0.gem", Label: labelGemPublish},
		{Command: "gem yank pkg -v 1.0.0", Label: labelGemPublish},
		{Command: "twine upload dist/*", Label: labelTwinePublish},
		{Command: "python3 -m twine upload dist/*", Label: labelTwinePublish},
		{Command: "cargo publish", Label: labelCargoPublish},
		{Command: "cargo yank --vers 1.0.0", Label: labelCargoPublish},
	})
}

func TestPublishNotationVariants(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "/opt/homebrew/bin/npm publish", Label: labelNPMPublish},
		{Command: "/usr/local/bin/gem push pkg-1.0.0.gem", Label: labelGemPublish},
		{Command: "true;npm publish", Label: labelNPMPublish},
		{Command: "cd pkg && cargo publish", Label: labelCargoPublish},
		{Command: "(npm publish)", Label: labelNPMPublish},
		{Command: "echo build\nnpm publish", Label: labelNPMPublish},
		{Command: "npm publish\n", Label: labelNPMPublish},
	})
}

// --dry-run が付いていれば公開されないので通す。
func TestDryRunAnywhereAllows(t *testing.T) {
	check(t, "", hooktest.Commands(
		"npm publish --dry-run",
		"npm publish --dry-run --tag next",
		"cargo publish --dry-run",
		"gem push --dry-run pkg-1.0.0.gem",
		"twine upload --dry-run dist/*",
	))
}

// --- C. リモートリソースの恒久削除 ---

func TestRemoteDeleteGHAndCloud(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "gh repo delete o/r --yes", Label: labelGHDelete},
		{Command: "gh release delete v1.0 --yes", Label: labelGHDelete},
		{Command: "gh gist delete abc123", Label: labelGHDelete},
		{Command: "gh api -X DELETE repos/o/r/releases/1", Label: labelGHAPIDelete},
		{Command: "gcloud compute instances delete my-vm", Label: labelGcloudDelete},
		{Command: "aws s3 rb s3://bucket --force", Label: labelAWSS3},
		{Command: "aws s3 rm s3://bucket/key", Label: labelAWSS3},
		{Command: "terraform destroy", Label: labelTFDestroy},
		{Command: "terraform -chdir=infra destroy -auto-approve", Label: labelTFDestroy},
		{Command: "tofu destroy", Label: labelTFDestroy},
		{Command: "terraform apply -destroy", Label: labelTFApplyDestroy},
		{Command: "mysqladmin drop mydb", Label: labelMysqladmin},
	})
}

// DB クライアント経由のときだけ見る（大文字小文字は問わない）。
func TestRemoteDeleteDatabaseStatements(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "psql -c 'DROP TABLE users'", Label: labelDB},
		{Command: "psql -c 'drop table users'", Label: labelDB},
		{Command: "psql -c 'DROP DATABASE app'", Label: labelDB},
		{Command: "psql -c 'DROP SCHEMA public CASCADE'", Label: labelDB},
		{Command: "mysql -e 'TRUNCATE sessions'", Label: labelDB},
		{Command: "mariadb -e 'DROP TABLE t'", Label: labelDB},
		{Command: "sqlite3 app.db 'DROP TABLE t'", Label: labelDB},
		{Command: "psql\n-c 'DROP TABLE t'", Label: labelDB},
	})
}

// --method=DELETE / --method DELETE / -XDELETE を -X DELETE に寄せる。
func TestRemoteDeleteMethodNotationIsNormalized(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "gh api -X DELETE /repos/o/r", Label: labelGHAPIDelete},
		{Command: "gh api -XDELETE repos/o/r", Label: labelGHAPIDelete},
		{Command: "gh api --method DELETE /repos/o/r", Label: labelGHAPIDelete},
		{Command: "gh api --method=DELETE /repos/o/r/actions/secrets/FOO", Label: labelGHAPIDelete},
		{Command: "gh api -X delete /repos/o/r", Label: labelGHAPIDelete},
		{Command: "curl -X DELETE https://api.github.com/user/keys/1", Label: labelHTTPDelete},
		{Command: "curl -XDELETE https://api.github.com/repos/o/r", Label: labelHTTPDelete},
		{Command: "curl --method=DELETE https://storage.googleapis.com/b/o", Label: labelHTTPDelete},
		{Command: "curl https://s3.amazonaws.com/b/o -X DELETE", Label: labelHTTPDelete},
		{Command: "wget --method=DELETE https://api.github.com/repos/o/r", Label: labelHTTPDelete},
	})
}

func TestRemoteDeleteNotationVariants(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "/opt/homebrew/bin/terraform destroy", Label: labelTFDestroy},
		{Command: "/usr/local/bin/aws s3 rb s3://bucket", Label: labelAWSS3},
		{Command: "/usr/local/bin/psql -c 'DROP DATABASE app'", Label: labelDB},
		{Command: "true;terraform destroy", Label: labelTFDestroy},
		{Command: "git status&&gh repo delete o/r --yes", Label: labelGHDelete},
		{Command: "rtk gh repo delete o/r --yes", Label: labelGHDelete},
		{Command: "echo hi\ngh release delete v1 --yes", Label: labelGHDelete},
		{Command: "gcloud x-delete delete", Label: labelGcloudDelete},
	})
}

// `\b` を途中に持つ規則（gcloud・aws・mysqladmin・git）の、語の一部にすぎない形。
func TestRemoteDeleteLookalikes(t *testing.T) {
	check(t, "", hooktest.Commands(
		"gcloud compute instances undelete my-vm",
		"gcloud compute instances deletex my-vm",
		"gcloud x-deletex",
		"aws s3api xdelete-object",
		"mysqladmin undrop mydb",
		"gh api -X DELETEx /repos/o/r",
	))
}

// --- D. Git オブジェクトの物理破壊 ---

func TestDestructiveGit(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "git reflog expire --expire=now --all", Label: labelReflog},
		{Command: "git gc --prune=now", Label: labelGC},
		{Command: "git gc --aggressive --prune=all", Label: labelGC},
		{Command: "git stash clear", Label: labelStash},
		{Command: "git -c x=1 gc --prune=now", Label: labelGC},
		{Command: "git gc --prune=all\n", Label: labelGC},
	})
}

func TestGitNotationVariants(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "/usr/bin/git reflog expire --all", Label: labelReflog},
		{Command: "git -C /repo stash clear", Label: labelStash},
		{Command: "true;git stash clear", Label: labelStash},
		{Command: "rtk git gc --prune=now", Label: labelGC},
		{Command: "echo x\ngit reflog expire --expire=now --all", Label: labelReflog},
	})
}

// `\bgc\b` と `--prune=(?:now|all)\b` の境界。
func TestGitLookalikes(t *testing.T) {
	check(t, "", hooktest.Commands(
		"git gcx --prune=now",
		"git xgc --prune=now",
		"git gc --prune=nowx",
		"git gc --prune=now_",
		"git xreflog expire",
		"git stash clearx",
	))
}

// --- E. マシン上の不可逆消去 ---

func TestMachineErase(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "diskutil eraseDisk APFS Blank disk2", Label: labelDiskutil},
		{Command: "diskutil eraseVolume APFS Blank disk2s1", Label: labelDiskutil},
		{Command: "diskutil zeroDisk disk2", Label: labelDiskutil},
		{Command: "diskutil apfs deleteContainer disk2", Label: labelAPFS},
		{Command: "tmutil delete -p /Volumes/TM/backup", Label: labelTmutil},
	})
}

func TestMachineEraseNotationVariants(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "/usr/sbin/diskutil eraseVolume APFS Blank disk2s1", Label: labelDiskutil},
		{Command: "/usr/bin/tmutil delete -p /Volumes/TM/x", Label: labelTmutil},
		{Command: "true;tmutil delete -p /Volumes/TM/x", Label: labelTmutil},
		{Command: "echo x\ndiskutil eraseDisk APFS Blank disk2", Label: labelDiskutil},
	})
}

// --- F. 秘密ファイル ---

func TestSecretFileDelete(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "rm .envrc", Label: labelSecretRM},
		{Command: "rm -f /Users/alice/dotfiles/.envrc", Label: labelSecretRM},
		{Command: "rm .env", Label: labelSecretRM},
		{Command: "shred -u .env.local", Label: labelSecretRM},
		{Command: "truncate -s 0 .envrc", Label: labelSecretRM},
		{Command: "unlink ~/.ssh/id_rsa", Label: labelSecretRM},
		{Command: "rm ~/.ssh/id_ed25519", Label: labelSecretRM},
		{Command: "rm -rf ~/.ssh", Label: labelSecretRM},
		{Command: "rm certs/server.pem", Label: labelSecretRM},
		{Command: "srm secrets/key.pem", Label: labelSecretRM},
		{Command: "rm ~/.config/dotfiles/envrc.local", Label: labelSecretRM},
	})
}

func TestSecretFileOverwrite(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "sed -i '' '/GH_TOKEN/d' .envrc", Label: labelSecretInPlace},
		{Command: "sed -i.bak 's/a/b/' .envrc", Label: labelSecretInPlace},
		{Command: "perl -i -pe 's/x/y/' .env.production", Label: labelSecretInPlace},
		{Command: "mv ~/.config/dotfiles/envrc.local /tmp/", Label: labelSecretMV},
		{Command: "mv .envrc /tmp/x", Label: labelSecretMV},
		{Command: "echo FOO=1 > .env", Label: labelSecretRedirect},
		{Command: "echo x >.env", Label: labelSecretRedirect}, // 空白なしのリダイレクト
		{Command: "printf 'x' > /Users/alice/dev/app/.envrc", Label: labelSecretRedirect},
		{Command: "cat other | tee .envrc", Label: labelSecretTee},
		{Command: "tee .env < input", Label: labelSecretTee},
	})
}

func TestSecretFileNotationVariants(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "/bin/rm .envrc", Label: labelSecretRM}, // パス起動
		{Command: "true;rm .envrc", Label: labelSecretRM},
		{Command: "(rm .envrc)", Label: labelSecretRM},
		{Command: "cat x\nrm .env", Label: labelSecretRM},
		{Command: "cd /repo && rm .envrc", Label: labelSecretRM},
		{Command: "mv -f -- '.envrc' /tmp/x", Label: labelSecretMV},
		{Command: "/usr/bin/mv .env /tmp/x", Label: labelSecretMV},
		{Command: "tee -a - \".env\"", Label: labelSecretTee},
		{Command: "echo x >  '.env.local'", Label: labelSecretRedirect},
		{Command: "rm .env\n", Label: labelSecretRM},
		{Command: "rm\u3000.envrc", Label: labelSecretRM},
	})
}

// 移植元が否定の先読み・後読みで除いていた形。@ の直前で名前を切るバックトラックも移植元と同じにする。
func TestSecretFileLookaroundBoundaries(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "rm .env.examples", Label: labelSecretRM + " (.env.examples)"},
		{Command: "rm .env.example.bak", Label: labelSecretRM + " (.env.example.bak)"},
		{Command: "rm ~/.ssh/configx", Label: labelSecretRM + " (~/.ssh/configx)"},
		{Command: "rm ~/.ssh/config.d", Label: labelSecretRM + " (~/.ssh/config.d)"},
		{Command: "rm ~/.ssh/id@x.pub", Label: labelSecretRM + " (~/.ssh/id)"},
		{Command: "rm x.pem@y", Label: labelSecretRM + " (x.pem)"},
		{Command: "rm .envrc.local", Label: labelSecretRM + " (.envrc.local)"},
		{Command: "rm .env.日本", Label: labelSecretRM + " (.env.日本)"},
	})
	check(t, "", hooktest.Commands(
		"rm .env.example",
		"rm .env..x",
		"rm .env/x",
		"rm .envx",
		"rm .envé",
		"rm ~/.ssh/known_hostsx",
		"rm ~/.ssh/config",
		"rm ~/.ssh/x.pub",
		"rm ~/.ssh/.pub",
		"rm x.pem/y",
		"rm x.pemé",
		"echo x >> .env",
		"echo x >>.env",
	))
}

// --- G. ガードファイル ---

// hhx の実行ファイル・設定ディレクトリ・両 CLI の hook の登録ファイルの削除と移動を止める。
func TestGuardRemoval(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "rm ~/.local/bin/hhx", Label: labelGuard + " (~/.local/bin/hhx)"},
		{Command: "rm -f /Users/alice/.local/bin/hhx", Label: labelGuard + " (/Users/alice/.local/bin/hhx)"},
		{Command: "mv $HOME/.local/bin/hhx /tmp/", Label: labelGuard},
		{Command: "trash \"$HOME/.local/bin/hhx\"", Label: labelGuard},
		{Command: "rm -rf ~/.config/hhx", Label: labelGuard + " (~/.config/hhx)"},
		{Command: "rm ~/.config/hhx/config.yaml", Label: labelGuard + " (~/.config/hhx/config.yaml)"},
		{Command: "mv /Users/alice/.config/hhx /tmp/", Label: labelGuard},
		{Command: "rm -rf ${HOME}/.config/hhx", Label: labelGuard + " (${HOME}/.config/hhx)"},
		{Command: "rm -rf //Users/alice/.config/hhx", Label: labelGuard + " (//Users/alice/.config/hhx)"},
		{Command: "rm ~/.codex/hooks.json", Label: labelGuard},
		{Command: "rm ~/.codex/config.toml", Label: labelGuard},
		{Command: "rm ~/.claude/settings.json", Label: labelGuard},
		{Command: "rm -f ~/.claude/settings.local.json", Label: labelGuard},
		{Command: "mv /Users/alice/.codex/hooks.json /Users/alice/.Trash/x/", Label: labelGuard},
		{Command: "trash ~/.codex/config.toml", Label: labelGuard},
		// キャッシュ名を含むトークンを読み飛ばしたあとも、その次のトークンを照合する。
		{Command: "rm ~/.config/hhx/__pycache__ .claude/settings.json", Label: labelGuard + " (.claude/settings.json)"},
		{Command: "rm ~/.config/hhx/__pycache__ .codex/hooks.json", Label: labelGuard + " (.codex/hooks.json)"},
		{Command: "rm ~/.config/hhx/__pycache__ ~/.config/hhx", Label: labelGuard + " (~/.config/hhx)"},
	})
}

func TestGuardNotationVariants(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "/bin/rm ~/.claude/settings.local.json", Label: labelGuard},
		{Command: "true;rm ~/.claude/settings.json", Label: labelGuard},
		{Command: "echo x\nrm ~/.codex/hooks.json", Label: labelGuard},
		{Command: "rm ~/.local/bin/hhx\n", Label: labelGuard},
		{Command: "(rm ~/.local/bin/hhx)", Label: labelGuard},
		{Command: "rm -rf ~/.config/hhx/", Label: labelGuard},
	})
}

// 移植元が守っていた旧配布先と dotfiles の正本は、hook の実体がもう無いので対象から外した。
func TestGuardLegacyLocationsAreNotGuarded(t *testing.T) {
	check(t, "", hooktest.Commands(
		"rm -rf ~/.codex/hooks",
		"rm ~/.claude/hooks/irreversible-guard.py",
		"mv ~/.claude/hooks ~/.Trash/",
		"rm -rf /Users/alice/.codex/hooks",
		"mv ~/.claude/hooks/pr-merge-guard.py /tmp/",
		"rm files/agent-config/claude/hooks/irreversible-guard.py",
		"rm -rf ~/dotfiles/files/agent-config/claude/hooks",
		"rm files/agent-config/claude/settings.common.json",
		"mv files/agent-config/codex/hooks.json /tmp/",
	))
}

// 保護対象と名前が前方一致するだけのファイル、別の場所にある同名のパス、編集・閲覧・キャッシュの掃除は通す。
func TestGuardLookalikesAndEditsPass(t *testing.T) {
	check(t, "", hooktest.Commands(
		"rm ~/.local/bin/hhx.bak",
		"rm ~/.local/bin/hhx-old",
		"rm ~/.config/hhx.bak",
		"rm -rf ~/.config/hhxfoo",
		"rm -rf ~/.config/hhx/__pycache__",
		"rm -rf /tmp/project/.config/hhx",
		"rm -rf project/.config/hhx",
		"rm /tmp/project/.local/bin/hhx",
		"rm -rf /tmp/x/~/.config/hhx",
		"rm -rf /tmp/Users/alice/.config/hhx",
		"cp ~/.local/bin/hhx /tmp/hhx",
		"vim ~/.config/hhx/config.yaml",
		"cat ~/.config/hhx/config.yaml",
		"hhx install",
		"hhx hook irreversible-guard 'rm x'",
		"cat ~/.claude/settings.json",
		"grep -n hooks ~/.claude/settings.json",
		"ls ~/.codex",
	))
}

// 実行ファイルは symlink を解決した実体でも照合する。ホームの外にあれば絶対パスで照合する。
func TestGuardFollowsTheExecutable(t *testing.T) {
	directory := t.TempDir()
	realPath := directory + "/opt/hhx-1.0/hhx"
	link := directory + "/bin/hhx"
	if err := os.MkdirAll(directory+"/opt/hhx-1.0", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(directory+"/bin", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realPath, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, link); err != nil {
		t.Fatal(err)
	}
	saved := executable
	t.Cleanup(func() { executable = saved })
	executable = func() (string, error) { return link, nil }
	resolvedReal, err := filepath.EvalSymlinks(realPath)
	if err != nil {
		t.Fatal(err)
	}
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "rm " + link, Label: labelGuard + " (" + link + ")"},
		{Command: "rm " + resolvedReal, Label: labelGuard},
	})
	check(t, "", hooktest.Commands("rm ~/.local/bin/hhx", "rm "+directory+"/bin/hhx2"))
}

// HOME が無くてもシェルは ~ をパスワードデータベースのホームへ展開するので、hhx の設定ディレクトリを守り続ける。
func TestGuardWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	if err := os.Unsetenv("HOME"); err != nil {
		t.Fatal(err)
	}
	home := py.Home()
	if home == "~" {
		t.Skip("パスワードデータベースからホームを得られない")
	}
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "rm -rf ~/.config/hhx", Label: labelGuard + " (~/.config/hhx)"},
		{Command: "rm -rf " + strings.TrimRight(home, "/") + "/.config/hhx", Label: labelGuard},
	})
}

// HOME が空なら ~ は空に展開されるので、/.config/hhx を守る。
func TestGuardWithEmptyHome(t *testing.T) {
	t.Setenv("HOME", "")
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "rm -rf ~/.config/hhx", Label: labelGuard + " (~/.config/hhx)"},
		{Command: "rm -rf /.config/hhx", Label: labelGuard},
	})
}

// --- 通過側 ---

func TestAllowsUnrelatedCommands(t *testing.T) {
	check(t, "", hooktest.Commands(
		"ls -la", "npm install", "npm run build", "npm run publish-docs", "npm run env-check", "yarn install",
		"cargo build --release", "gem install rails", "python3 -c 'print(1)'", "make merge", "direnv allow",
	))
}

func TestAllowsReadOnlyAndReversible(t *testing.T) {
	check(t, "", hooktest.Commands(
		"git status", "git log --oneline -5", "git stash", "git stash pop", "git stash list",
		"git stash drop stash@{0}", "git reflog", "git gc", "git gc --prune=2.weeks.ago", "git fetch --all --prune",
		"git push --force-with-lease origin HEAD", // 履歴から戻せるので対象外
		"git push origin :old-branch", "git branch -D old",
	))
}

func TestAllowsGHNonDestructive(t *testing.T) {
	check(t, "", hooktest.Commands(
		"gh pr view 12", "gh auth status", "gh secret set GH_TOKEN", "gh secret list", "gh variable list",
		"gh release create v1 --notes x", "gh release view v1", "gh repo view o/r", "gh repo clone o/r",
		"gh api repos/o/r", "gh api repos/o/r/actions/secrets", "gh api -X PATCH repos/o/r/issues/1 -f state=closed",
		"gh api -X PUT repos/o/r/issues/1/labels",
		"gh cache delete --all", // キャッシュは再生成できる
	))
}

func TestAllowsCloudNonDestructive(t *testing.T) {
	check(t, "", hooktest.Commands(
		"gcloud auth list", "gcloud config set project x", "gcloud compute instances list", "gcloud storage ls",
		"aws sts get-caller-identity", "aws s3 ls s3://bucket", "aws s3 cp x s3://bucket/",
		"aws s3 sync ./dist s3://bucket", "terraform plan",
		"terraform plan -destroy", // 計画の表示だけ
		"terraform apply", "terraform apply -auto-approve",
	))
}

func TestAllowsHTTPWithoutDestructiveIntent(t *testing.T) {
	check(t, "", hooktest.Commands(
		"curl -s https://api.github.com/repos/o/r",
		"curl -X POST https://api.github.com/repos/o/r/issues -f title=x",
		"curl -X DELETE https://example.com/api/session", // クラウド API ではない
		"wget https://example.com/x.tar.gz",
	))
}

func TestAllowsCredentialToolsNonDestructive(t *testing.T) {
	check(t, "", hooktest.Commands(
		"security find-generic-password -a alice -s gh-token -w", "security add-generic-password -a x -s y -w z",
		"op item get 'GitHub PAT'", "op read op://vault/item/field", "gpg --list-secret-keys",
		"gpg --delete-key ABCD", // 公開鍵は再取得できる
		"npm token list", "twine check dist/*", "gem list", "diskutil list", "diskutil info /", "tmutil status",
		"tmutil listbackups",
	))
}

func TestAllowsDatabaseReadOnly(t *testing.T) {
	check(t, "", hooktest.Commands(
		"psql -c 'select * from truncate_log'", // truncate が語の一部
		"psql -f schema.sql", "mysql -e 'select 1'", "sqlite3 db.sqlite '.tables'",
		"grep -rn 'DROP TABLE' db/migrate", // DB クライアントではない
		"psql -c 'xDROP TABLE t'",
	))
}

func TestAllowsSecretFilesNonDestructive(t *testing.T) {
	check(t, "", hooktest.Commands(
		"cat .envrc", "source .envrc", "grep GH_TOKEN .envrc", "direnv allow .envrc",
		"cat .env > /tmp/backup", // 読む側であって上書き先ではない
		"echo x >> .env",         // 追記は既存の値を壊さない
		"diff .env .env.example", "chmod 600 ~/.ssh/id_ed25519", "ssh-add ~/.ssh/id_ed25519", "ls -la ~/.ssh",
		"cat ~/.ssh/config", "vim ~/.ssh/config", "ssh-keygen -R example.com",
		"rm ~/.ssh/known_hosts",    // 再生成できる
		"rm ~/.ssh/id_ed25519.pub", // 秘密鍵から作り直せる
	))
}

func TestAllowsEnvTemplates(t *testing.T) {
	check(t, "", hooktest.Commands(
		"rm .env.example", "rm .env.sample", "rm .env.template", "rm .env.dist", "cat .env.example",
		"cp .env.example .env.example.bak",
		"mv .env.example .env", // 作る側なので通す
		"git add .env.example", "rm -rf node_modules && cp .env.example .env",
		"rm src/environment.ts", // env が語の一部
	))
}

// クォート内・grep の対象文字列は境界に含めない。
func TestAllowsQuotedAndSearchForms(t *testing.T) {
	check(t, "", hooktest.Commands(
		"echo 'npm publish'", "echo 'rm .envrc'", "grep -rn 'credentials/revoke' logs/", "rg 'gh auth logout' docs/",
		"grep -rn 'gh secret delete' docs/", "grep -rn 'terraform destroy' notes.md",
	))
}

// 二次ゲートは「動詞語 OR 秘密/ガードパス片」でしか先へ進めない。
// 一次ゲートを通す語 (revoke など) を含んでも、動詞語も秘密パスも無ければ抜ける。
func TestSecondaryGateLimitsScope(t *testing.T) {
	check(t, "", hooktest.Commands("grep -rn revoke .", "echo revoke", "rg 'credentials/revoke'"))
}

// 一次ゲートの語を含まない入力は判定しない。
func TestPrimaryGate(t *testing.T) {
	for input, want := range map[string]bool{
		"ls -la": false, "echo ok": false, "make merge": false, "rm x": false,
		"gh x": true, "x.env": true, "op item": true, "hhx": true, "~/.claude/x": true, "agent-config": false,
	} {
		if got := gate([]byte(input)); got != want {
			t.Errorf("gate(%q)=%v, want %v", input, got, want)
		}
	}
}

// --- Edit / Write / apply_patch ---

func fileTool(t *testing.T, toolName string, toolInput map[string]any) hooktest.Result {
	t.Helper()
	return hooktest.Stdin(t, Definition(), hooktest.ToolPayload(toolName, toolInput, "/tmp"))
}

func TestFileToolSecretFile(t *testing.T) {
	for _, path := range []string{
		"/Users/alice/dev/app/.envrc", "/Users/alice/dev/app/.env", "/Users/alice/dev/app/.env.production",
		"/Users/alice/.config/dotfiles/envrc.local", "/Users/alice/dev/app/certs/server.pem",
		"/Users/alice/.ssh/id_ed25519",
	} {
		if got := fileTool(t, "Edit", map[string]any{"file_path": path}); got.Decision != hooktest.Deny ||
			!strings.Contains(got.Reason, labelSecretEdit) {
			t.Errorf("Edit %q: %+v", path, got)
		}
	}
	if got := fileTool(t, "MultiEdit", map[string]any{"file_path": "/x/.env"}); got.Decision != hooktest.Deny {
		t.Errorf("MultiEdit: %+v", got)
	}
	if got := fileTool(t, "NotebookEdit", map[string]any{"notebook_path": "/x/.envrc"}); got.Decision != hooktest.Deny {
		t.Errorf("NotebookEdit: %+v", got)
	}
	// 存在しない秘密ファイルでも、Write による新規作成を塞ぐ。
	if got := fileTool(t, "Write", map[string]any{"file_path": t.TempDir() + "/nope/.env"}); got.Decision != hooktest.Deny {
		t.Errorf("Write to a missing path: %+v", got)
	}
	// file_path が偽の値なら notebook_path を見る（移植元の `a or b`）。
	for _, falsy := range []any{"", nil, 0, false, []any{}} {
		input := map[string]any{"file_path": falsy, "notebook_path": "/x/.env"}
		if got := fileTool(t, "Edit", input); got.Decision != hooktest.Deny {
			t.Errorf("Edit with file_path=%v: %+v", falsy, got)
		}
	}
}

func TestFileToolNonSecretFile(t *testing.T) {
	for _, path := range []string{
		"/x/.env.example", "/x/.env.sample", "/x/.env.template", "/x/.env.dist", "/x/src/environment.ts",
		"/Users/alice/.ssh/config", "/Users/alice/.ssh/known_hosts", "/Users/alice/.ssh/id_ed25519.pub",
		"/Users/alice/.claude/settings.json", "/Users/alice/.local/bin/hhx",
	} {
		if got := fileTool(t, "Edit", map[string]any{"file_path": path}); got.Decision != "" {
			t.Errorf("Edit %q: %+v", path, got)
		}
	}
	// 真の値でも文字列でなければ判定しない。
	if got := fileTool(t, "Write", map[string]any{"file_path": []any{".env"}}); got.Decision != "" {
		t.Errorf("Write with a list path: %+v", got)
	}
}

func TestApplyPatch(t *testing.T) {
	for _, header := range []string{
		"*** Update File: .envrc", "*** Delete File: .envrc", "*** Update File: config/.env.production",
		"*** Delete File: config/production.pem", "*** Add File: .env", "*** Add File: config/secret.pem",
		"*** Add File: /Users/alice/no-such-secret-hook-test.pem", "*** Add File:  .env  ",
	} {
		patch := "*** Begin Patch\n" + header + "\n@@\n-a\n+b\n*** End Patch"
		got := fileTool(t, "apply_patch", map[string]any{"input": patch})
		if got.Decision != hooktest.Deny || !strings.Contains(got.Reason, labelSecretPatch) {
			t.Errorf("apply_patch %q: %+v", header, got)
		}
	}
	for _, header := range []string{
		"*** Update File: .env.example", "*** Update File: src/app.ts", "*** Delete File: .env.sample",
		// パスは JSON でエスケープされる文字の手前までになる（移植元は JSON 文字列に当てていた）。
		"*** Add File: a\".env", "*** Add File: a\\.env", "*** Add File: a\t.env",
	} {
		patch := "*** Begin Patch\n" + header + "\n@@\n-a\n+b\n*** End Patch"
		if got := fileTool(t, "apply_patch", map[string]any{"input": patch}); got.Decision != "" {
			t.Errorf("apply_patch %q: %+v", header, got)
		}
	}
}

// tool_input の中の文字列は、キーも含めて JSON の順に辿る。重複したキーは最初の位置に最後の値を置く（Python の dict）。
func TestJSONStringsFollowPythonDictOrder(t *testing.T) {
	raw := json.RawMessage(`{"b": ["x", {"k": "v"}], "a": 1, "b": "y", "c": null, "d": "z"}`)
	if got, want := jsonStrings(raw), []string{"b", "y", "a", "c", "d", "z"}; !reflect.DeepEqual(got, want) {
		t.Errorf("jsonStrings=%q, want %q", got, want)
	}
	payload := `{"tool_name": "apply_patch", "tool_input": {"a": "*** Add File: a.pem", ` +
		`"b": "*** Add File: .env", "a": "*** Add File: .envrc"}}`
	if got := hooktest.Stdin(t, Definition(), payload); !strings.Contains(got.Reason, "(.envrc)") {
		t.Errorf("duplicate key: %+v", got)
	}
	payload = `{"tool_name": "apply_patch", "tool_input": {"*** Delete File: x.pem": [1]}}`
	if got := hooktest.Stdin(t, Definition(), payload); !strings.Contains(got.Reason, "(x.pem)") {
		t.Errorf("key: %+v", got)
	}
}

func TestReadToolIsNotBlocked(t *testing.T) {
	if got := fileTool(t, "Read", map[string]any{"file_path": "/Users/alice/dev/app/.envrc"}); got.Decision != "" {
		t.Errorf("Read: %+v", got)
	}
	input := map[string]any{"pattern": "TOKEN", "path": "/Users/alice/dev/app/.envrc"}
	if got := fileTool(t, "Grep", input); got.Decision != "" {
		t.Errorf("Grep: %+v", got)
	}
}

// --- L2: 関数単位 ---

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"gh api --method=DELETE x":   "gh api -X DELETE x",
		"gh api --method DELETE x":   "gh api -X DELETE x",
		"gh api --method   DELETE x": "gh api -X DELETE x",
		"curl -XDELETE x":            "curl -X DELETE x",
		"curl -XPOST -XDELETE x":     "curl -X POST -X DELETE x",
		"curl -X DELETE x":           "curl -X DELETE x",
		"curl -Xfoo x":               "curl -X foo x",
		"a\nb":                       "a b",
		"echo x\nrm .envrc":          "echo x rm .envrc",
		// 似たフラグは書き換えない。
		"gh api --methodology=DELETE x": "gh api --methodology=DELETE x",
		"cmd --methods DELETE":          "cmd --methods DELETE",
		"cmd -x DELETE":                 "cmd -x DELETE",
		"rm .envrc":                     "rm .envrc",
	}
	for source, want := range cases {
		if got := normalize(source); got != want {
			t.Errorf("normalize(%q)=%q, want %q", source, got, want)
		}
		if once := normalize(source); normalize(once) != once {
			t.Errorf("normalize is not idempotent for %q", source)
		}
	}
}

func TestSecretPathLabel(t *testing.T) {
	for path, want := range map[string]string{
		"/x/.envrc": ".envrc", "/x/.env": ".env", "/x/.env/": ".env", "/x/.env.local": ".env.local",
		"/x/.env.production": ".env.production", "/x/envrc.local": "envrc.local",
		"/Users/alice/.config/dotfiles/envrc.local": "envrc.local", "certs/server.pem": "server.pem",
		"~/.ssh/id_ed25519": ".ssh/id_ed25519", "/Users/alice/.ssh/id_rsa": ".ssh/id_rsa",
		`C:\Users\alice\.ssh\id_rsa`: `.ssh/C:\Users\alice\.ssh\id_rsa`, "/x/.ssh/": ".ssh/.ssh",
		// 非表示の秘密ファイルとして除外しない形。
		"/x/.env.examples": ".env.examples", "/x/.env.example.bak": ".env.example.bak",
		// 除外する形。
		"/x/.env.example": "", "/x/.env.sample": "", "/x/.env.template": "", "/x/.env.dist": "",
		"/x/.environment": "", "src/env.ts": "", "/x/README.md": "", "~/.ssh/id_ed25519.pub": "",
		"~/.ssh/known_hosts": "", "~/.ssh/known_hosts.old": "", "~/.ssh/config": "", "/x/.ssh": "",
		"/x/.claude/settings.json": "", "": "",
	} {
		if got := secretPathLabel(path); got != want {
			t.Errorf("secretPathLabel(%q)=%q, want %q", path, got, want)
		}
	}
	for _, value := range []any{nil, 123.0, []any{".env"}, map[string]any{"file_path": ".env"}} {
		if got := secretPathLabel(value); got != "" {
			t.Errorf("secretPathLabel(%v)=%q", value, got)
		}
	}
}

func TestSecretTokens(t *testing.T) {
	for segment, want := range map[string][]string{
		" rm .envrc":           {".envrc"},
		"rm .env":              {".env"},
		"cat .env.local":       {".env.local"},
		"mv envrc.local /tmp":  {"envrc.local"},
		"rm certs/server.pem":  {"certs/server.pem"},
		"rm ~/.ssh/id_ed25519": {"~/.ssh/id_ed25519"},
		"rm -rf ~/.ssh":        {"~/.ssh"},
		"cat /Users/alice/.config/dotfiles/envrc.local": {"/Users/alice/.config/dotfiles/envrc.local"},
		"rm '.envrc'":                 {".envrc"},
		"FOO=.env ls":                 {".env"},
		"echo x > .env":               {".env"},
		"echo x >.env":                {".env"}, // 空白なしのリダイレクトも拾う
		".env":                        {".env"}, // 先頭は ^ で拾う
		"a/.env b/.ssh/ c.pem":        {"a/.env", "b/.ssh/", "c.pem"},
		"x/.env/.pem y":               {"x/.env/.pem"},
		"rm ~/.ssh/id@host":           {"~/.ssh/id@host"},
		"rm ~/.ssh/id@x.pub":          {"~/.ssh/id"},
		"rm =.env":                    {".env"},
		"cat .env.example":            nil,
		"rm .env.sample":              nil,
		"ls .environment":             nil,
		"npm run env":                 nil,
		"rm ~/.ssh/known_hosts":       nil,
		"rm ~/.ssh/id_ed25519.pub":    nil,
		"echo hi":                     nil,
		"cat ~/.claude/settings.json": nil,
		"x.env":                       nil,
	} {
		if got := newSecretScanner(segment).tokens(); !reflect.DeepEqual(got, want) {
			t.Errorf("tokens(%q)=%q, want %q", segment, got, want)
		}
	}
}

// --- 入出力の契約 ---

func TestOutputSchema(t *testing.T) {
	got := hooktest.Stdin(t, Definition(), hooktest.BashPayload("gh auth logout", "/tmp", nil))
	if got.Decision != hooktest.Deny {
		t.Fatalf("decision=%q", got.Decision)
	}
	if want := reason(hooktest.Language, target{id: idGHLogout}, idWhyCred); got.Reason != want {
		t.Errorf("reason=%q, want %q", got.Reason, want)
	}
}

// 移植元との違い: ファイルへ書き出すだけの here-document の本文は、このコマンドの中では実行されないので判定しない。
func TestIgnoresHeredocWrittenToFile(t *testing.T) {
	check(t, "", hooktest.Commands(
		"cat > /tmp/v.sh <<'EOF'\nopenssl x509 -in a > $S/out/one.pem\nrm .envrc\nEOF",
		"cat >> /tmp/v.sh <<\"EOF\"\ncat x > key.pem\nEOF",
		"cat <<'EOF' > /tmp/v.sh\ngh auth logout\nEOF",
		"cat > /tmp/v.sh <<EOF\nrm .envrc\nEOF",
		"cat > /tmp/v.sh <<-'EOF'\n\trm .envrc\n\tEOF",
		"tee /tmp/v.sh >/dev/null <<'EOF'\nnpm publish\nEOF",
		"/bin/cat >/tmp/v.sh <<\\EOF\nrm ~/.ssh/id_rsa\nEOF\necho done",
		// 複数の here-document と、後ろに続く安全なコマンド。
		"cat > a.sh <<'A'\nrm .envrc\nA\ncat > b.sh <<'B'\nnpm publish\nB\nls",
	))
}

// 本文を実行しうる形と、範囲を確かめられない形は、従来どおり本文も判定する。
func TestStillChecksHeredocThatMayRun(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		// 書き出したスクリプトを同じコマンドで実行する。
		{Command: "cat > /tmp/v.sh <<'EOF'\nrm .envrc\nEOF\nbash /tmp/v.sh", Label: labelSecretRM},
		// シェルや他のコマンドへ流し込む。
		{Command: "bash <<'EOF'\nrm .envrc\nEOF", Label: labelSecretRM},
		{Command: "ssh host <<'EOF'\nrm .envrc\nEOF", Label: labelSecretRM},
		// クォートしていない本文の $(...) とバッククォートは、外側のシェルが実行する。
		{Command: "cat > /tmp/v.sh <<EOF\n$(rm .envrc)\nEOF", Label: labelSecretRM},
		// 区切りの行が無い。
		{Command: "cat > /tmp/v.sh <<'EOF'\nrm .envrc", Label: labelSecretRM},
		// クォートの中の << は演算子ではない。本文と取り違えず、後ろのコマンドを判定する。
		{Command: "echo \"a <<EOF\"\nrm .envrc\nEOF", Label: labelSecretRM},
		// 本文の後ろのコマンドは判定する。
		{Command: "cat > /tmp/v.sh <<'EOF'\nls\nEOF\nrm .envrc", Label: labelSecretRM},
		// 書き出し先そのものが秘密ファイルなら、本文の外の演算子の行で判定する。
		{Command: "cat > .envrc <<'EOF'\nexport A=1\nEOF", Label: labelSecretRedirect},
	})
}

func TestStripWrittenHeredocs(t *testing.T) {
	for command, want := range map[string]string{
		"cat > a.sh <<'EOF'\nrm x\nEOF\nls":                    "cat > a.sh <<'EOF'\nEOF\nls",
		"cat > a.sh <<'EOF'\nrm x\nEOF\nsh a.sh":               "cat > a.sh <<'EOF'\nrm x\nEOF\nsh a.sh",
		"cat > d/a.sh <<'EOF'\nrm x\nEOF\n./a.sh":              "cat > d/a.sh <<'EOF'\nrm x\nEOF\n./a.sh",
		"grep x <<'EOF'\nrm x\nEOF":                            "grep x <<'EOF'\nrm x\nEOF",
		"cat > a.sh <<'A' && cat > b.sh <<'B'\n1\nA\n2\nB\nls": "cat > a.sh <<'A' && cat > b.sh <<'B'\nA\nB\nls",
		// 名前は部分文字列で数えるので、短い名前は他の語に紛れて外さない側に倒れる。
		"cat > a <<'EOF'\nrm x\nEOF": "cat > a <<'EOF'\nrm x\nEOF",
		// クォートしていない本文のバッククォートは、外側のシェルが実行する。
		"cat > a.sh <<EOF\n`x`\nEOF":            "cat > a.sh <<EOF\n`x`\nEOF",
		"cat <<< 'rm x' > a":                    "cat <<< 'rm x' > a",
		"cat > a.sh <<'EOF'\r\nrm x\r\nEOF\r\n": "cat > a.sh <<'EOF'\r\nEOF\r\n",
	} {
		if got := stripWrittenHeredocs(command); got != want {
			t.Errorf("stripWrittenHeredocs(%q)=%q, want %q", command, got, want)
		}
	}
}

// DB の破壊は、ローカルの DB ファイル（sqlite3）もあるので、リモートの削除とは別の理由文にする。
func TestDatabaseReason(t *testing.T) {
	for command, id := range map[string]string{
		"sqlite3 app.db 'DROP TABLE t'": idDB,
		"mysqladmin drop mydb":          idMysqladmin,
	} {
		got := hooktest.Stdin(t, Definition(), hooktest.BashPayload(command, "/tmp", nil))
		if want := reason(hooktest.Language, target{id: id}, idWhyDB); got.Reason != want {
			t.Errorf("%s: reason=%q, want %q", command, got.Reason, want)
		}
	}
}

// Codex は ask を解釈しないので、判定は deny だけに揃える。
func TestNeverUsesAsk(t *testing.T) {
	check(t, hooktest.Deny, hooktest.Commands("gh auth logout", "npm publish", "rm .envrc", "trash ~/.codex/config.toml"))
}

func TestArgvDebugPath(t *testing.T) {
	for command, want := range map[string]string{
		"gh auth logout": hooktest.Deny, "rm .envrc": hooktest.Deny, "git status": "", "cat .envrc": "",
		// argv の文字列は payload として読まず、そのままコマンドとして判定する。
		`{"tool_name": "Edit", "tool_input": {"file_path": ".env"}}`: "",
	} {
		if got := hooktest.Argv(t, Definition(), command); got.Decision != want {
			t.Errorf("argv %q: %+v, want %q", command, got, want)
		}
	}
}

func TestOddInputsDoNotCrash(t *testing.T) {
	for _, raw := range []string{
		"", "   ", "not json at all", "[]", "null", "0", `"just a string"`,
		`{"tool_input": {"command": null}}`, `{"tool_input": {"command": 123}}`, `{"tool_input": "rm .envrc"}`,
		`{"tool_input": {"command": ["rm .envrc"]}}`, `{"tool_name": "Edit", "tool_input": null}`,
		`{"tool_name": "Write", "tool_input": {"file_path": null}}`,
		`{"tool_name": "Write", "tool_input": {"file_path": [".env"]}}`,
		`{"tool_name": "apply_patch", "tool_input": {"input": null}}`,
		`{"tool_name": "apply_patch", "tool_input": {"input": ".envrc"}}`, `{"cwd": "/tmp"}`,
	} {
		if got := hooktest.Stdin(t, Definition(), raw); got.Decision != "" {
			t.Errorf("odd input %q: %+v", raw, got)
		}
	}
}

// パースできない入力は生のまま判定に回す（取り逃がしより過検出を選ぶ）。
func TestBrokenJSONIsNotFailOpen(t *testing.T) {
	for _, raw := range []string{"gh auth logout", "rm .envrc", `[" rm .envrc"]`, `{"tool_input": {"command": "x"}} ; rm .envrc`} {
		if got := hooktest.Stdin(t, Definition(), raw); got.Decision != hooktest.Deny {
			t.Errorf("broken input %q: %+v", raw, got)
		}
	}
}

func TestMissingOrOddToolNameDefaultsToBash(t *testing.T) {
	for _, raw := range []string{
		`{"tool_input": {"command": "gh auth logout"}}`,
		`{"tool_name": "", "tool_input": {"command": "gh auth logout"}}`,
		`{"tool_name": null, "tool_input": {"command": "gh auth logout"}}`,
		`{"tool_name": 1, "tool_input": {"command": "gh auth logout"}}`,
	} {
		if got := hooktest.Stdin(t, Definition(), raw); got.Decision != hooktest.Deny {
			t.Errorf("%s: %+v", raw, got)
		}
	}
}

func TestLongCommandIsHandled(t *testing.T) {
	padding := strings.Repeat("x", 20000)
	check(t, "", hooktest.Commands("echo "+padding))
	check(t, hooktest.Deny, hooktest.Commands("echo "+padding+" && gh auth logout"))
}

// 病的に長い入力でも、正規表現と手書きの走査が破綻しない。
func TestPathologicalInputIsFast(t *testing.T) {
	cases := []string{
		"gh api " + strings.Repeat("x", 20000) + " repos/o/r",
		"curl " + strings.Repeat("-XDELETE ", 2000) + "https://example.com/x",
		"gh api " + strings.Repeat("--method=DELETE ", 2000) + "repos/o/r",
		"gh" + strings.Repeat(" ", 5000) + "pr view 1",
		"echo " + strings.Repeat(".env.example ", 2000), // 秘密ファイルの惜しい形の連打
		"cat .env.example " + strings.Repeat("a/", 5000) + "x",
		"ls " + strings.Repeat("a/", 3000) + " ~/.ssh/id_rsa",
		"rm " + strings.Repeat("/.pem", 4000) + "/x",
		"mv " + strings.Repeat("-/mv ", 3000) + "x",
		"rm .ssh/" + strings.Repeat(".pub@", 4000) + "/",
		"rm " + strings.Repeat(".env.", 4000) + "/",
	}
	start := time.Now()
	for _, command := range cases {
		hooktest.Stdin(t, Definition(), hooktest.BashPayload(command, "/tmp", nil))
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("took %v", elapsed)
	}
}

// Bash 以外の payload が来ても落ちない（matcher の設定漏れへの保険）。
func TestOtherToolPayload(t *testing.T) {
	for toolName, toolInput := range map[string]map[string]any{
		"Read": {"file_path": "/tmp/x"}, "Glob": {"pattern": "**/*.py"}, "WebFetch": {"url": "https://example.com"},
		"Task": {"prompt": "rm .envrc してください"},
	} {
		if got := fileTool(t, toolName, toolInput); got.Decision != "" {
			t.Errorf("%s: %+v", toolName, got)
		}
	}
}

func TestDisabledByConfig(t *testing.T) {
	got := hooktest.Run(t, Definition(), hooktest.BashPayload("gh auth logout", "/tmp", nil),
		hooktest.Options{Config: "hooks:\n  irreversible-guard:\n    enabled: false\n"})
	if got.Decision != "" {
		t.Fatalf("disabled hook must stay silent: %+v", got)
	}
}
