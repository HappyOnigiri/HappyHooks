package main

import (
	"github.com/HappyOnigiri/hhx/internal/config"
	"github.com/HappyOnigiri/hhx/internal/i18n"
)

// CLI の出力の文面である。コマンド名・フラグ・パス・状態の語（claude・codex）は訳さない。
// wait-ci の接頭辞 `wait-ci:` と最終行 `wait-ci: exit=...` は読み手との契約なので、ここに置かずコードに固定する。
const (
	idUsage          = "cli.usage"
	idUnknownCommand = "cli.unknown-command"

	idUnexpectedArgument = "cli.unexpected-argument"
	idUnknownAgent       = "cli.install.unknown-agent"
	idAgentFlag          = "cli.install.agent-flag"
	idNoAgentDirectory   = "cli.install.no-agent-directory"
	idUnchanged          = "cli.install.unchanged"
	idUpdated            = "cli.install.updated"
	idWroteSymlinkTarget = "cli.install.wrote-symlink-target"
	idBackupSaved        = "cli.install.backup-saved"
	idNotOnPath          = "cli.install.not-on-path"
	idUnknownHooks       = "cli.install.unknown-hooks"
	idInvalidLanguage    = "cli.install.invalid-language"

	idAllowUsage         = "cli.allow-generated-edit.usage"
	idAllowNoReason      = "cli.allow-generated-edit.no-reason"
	idAllowNoPaths       = "cli.allow-generated-edit.no-paths"
	idAllowUnknownOption = "cli.allow-generated-edit.unknown-option"
	idAllowDeclared      = "cli.allow-generated-edit.declared"
	idAllowDisabled      = "cli.allow-generated-edit.disabled"

	idApplyFlag      = "cli.update.apply-flag"
	idDevelopment    = "cli.update.development-build"
	idUpToDate       = "cli.update.up-to-date"
	idAvailable      = "cli.update.available"
	idUnsupported    = "cli.update.unsupported"
	idUpdating       = "cli.update.updating"
	idOtherOnPath    = "cli.update.other-on-path"
	idNoReleaseYet   = "cli.update.no-release"
	idWaitCIUsage    = "wait-ci.usage"
	idFlagSHA        = "wait-ci.flag.sha"
	idFlagAnySHA     = "wait-ci.flag.any-sha"
	idFlagInterval   = "wait-ci.flag.interval"
	idFlagTimeout    = "wait-ci.flag.timeout"
	idFlagStart      = "wait-ci.flag.start-timeout"
	idFlagSettle     = "wait-ci.flag.settle"
	idFlagNoCI       = "wait-ci.flag.no-ci-timeout"
	idFlagPRLookup   = "wait-ci.flag.pr-lookup-timeout"
	idFlagProgress   = "wait-ci.flag.progress"
	idFlagAllChecks  = "wait-ci.flag.all-checks"
	idIntervalTooLow = "wait-ci.interval-too-low"
	idToolRequired   = "wait-ci.tool-required"
	idSkippedNoPR    = "wait-ci.skipped-no-pr"
	idDetachedFailed = "wait-ci.detached-lookup-failed"
	idNoPRFor        = "wait-ci.no-pr-for"
	idTargetBranch   = "wait-ci.target.branch"
	idTargetCommit   = "wait-ci.target.commit"
	idGHKeptFailing  = "wait-ci.gh-kept-failing"
	idUnknownHead    = "wait-ci.unknown-head"
	idHeadTimeout    = "wait-ci.head-timeout"
	idConflictNoRun  = "wait-ci.conflict-no-checks"
	idNoCI           = "wait-ci.no-ci"
	idMainNoPR       = "wait-ci.main-no-pr"
	idEmptyTimeout   = "wait-ci.empty-timeout"
	idTimeout        = "wait-ci.timeout"
	idFailed         = "wait-ci.failed"
	idAllPassed      = "wait-ci.all-passed"
	idConflicting    = "wait-ci.conflicting"
)

var messages = i18n.Register(i18n.Catalog{
	idUsage: {
		EN: `Usage:
  hhx hook <name> [input]   Run a hook (invoked by Claude Code / Codex)
  hhx install [--agent claude|codex]...
                            Register hhx hooks in the agent settings
  hhx uninstall [--agent claude|codex]...
                            Remove hhx hooks from the agent settings
  hhx wait-ci [reference] [options]
                            Report the CI result of a pull request once every check has finished
  hhx allow-generated-edit --reason <text> <path>...
                            Declare a user-instructed hand edit of generated files
  hhx update [--apply]      Check GitHub Releases for a newer hhx (and install it)
  hhx version               Print the version
`,
		JA: `使い方:
  hhx hook <name> [input]   hook を実行する（Claude Code / Codex から呼ばれる）
  hhx install [--agent claude|codex]...
                            hhx の hook を agent の設定に登録する
  hhx uninstall [--agent claude|codex]...
                            hhx の hook を agent の設定から外す
  hhx wait-ci [reference] [options]
                            PR の全 check が終わったら、CI の結果を 1 回だけ報告する
  hhx allow-generated-edit --reason <text> <path>...
                            ユーザーが指示した生成ファイルの手編集を宣言する
  hhx update [--apply]      GitHub Releases で新しい hhx を確かめる（--apply で入れる）
  hhx version               版を表示する
`,
	},
	idUnknownCommand: {EN: "hhx: unknown command {{.Command}}\n\n", JA: "hhx: 未知のコマンド {{.Command}}\n\n"},
	idUnexpectedArgument: {
		EN: "hhx {{.Command}}: unexpected argument {{.Argument}}",
		JA: "hhx {{.Command}}: 余分な引数 {{.Argument}}",
	},
	idUnknownAgent: {
		EN: "hhx {{.Command}}: unknown agent {{.Agent}} (want claude or codex)",
		JA: "hhx {{.Command}}: 未知の agent {{.Agent}}（claude か codex を指定する）",
	},
	idAgentFlag: {
		EN: "agent to configure: claude or codex (repeatable; default: every agent whose config directory exists)",
		JA: "設定する agent: claude か codex（複数指定可。既定は設定ディレクトリのある agent すべて）",
	},
	idNoAgentDirectory: {
		EN: "hhx {{.Command}}: neither ~/.claude nor ~/.codex exists; pass --agent to choose explicitly",
		JA: "hhx {{.Command}}: ~/.claude も ~/.codex も無い。--agent で明示する",
	},
	// install / uninstall の結果の状態。
	idUnchanged: {EN: "unchanged", JA: "変更なし"},
	idUpdated:   {EN: "updated", JA: "更新"},
	idWroteSymlinkTarget: {
		EN: "  wrote the symlink target {{.Path}}; commit it where it is managed",
		JA: "  symlink の先 {{.Path}} に書いた。管理している場所でコミットする",
	},
	idBackupSaved: {EN: "  previous content saved to {{.Path}}", JA: "  変更前の内容を {{.Path}} に保存した"},
	idNotOnPath: {
		EN: "hhx is not on PATH; install it (e.g. make install) and add its directory to PATH first",
		JA: "hhx が PATH に無い。先にインストールし（make install など）、そのディレクトリを PATH に足す",
	},
	idUnknownHooks: {EN: "{{.Path}}: unknown hooks: {{.Hooks}}", JA: "{{.Path}}: 未知の hook: {{.Hooks}}"},
	// 不正な language は hook では英語に倒して動き続けるので、気付く機会は install しかない。
	idInvalidLanguage: {
		EN: "{{.Path}}: language must be en or ja, got {{.Value}}",
		JA: "{{.Path}}: language は en か ja で指定する（{{.Value}} は使えない）",
	},

	// 宣言の使い方。ユーザーの明示的な指示があるときだけ使うことと、記録は hook が行うことを伝える。
	idAllowUsage: {
		EN: "Usage: hhx allow-generated-edit --reason <the user's instruction> <path>...\n\n" +
			"Declare that the user explicitly told the agent to hand-edit these generated files. generated-edit-guard " +
			"records the declaration for the agent session when it sees this command, and allows edits of the paths " +
			"for the rest of the session. Run it only on the user's explicit instruction.\n",
		JA: "使い方: hhx allow-generated-edit --reason <ユーザーの指示> <path>...\n\n" +
			"ユーザーが生成ファイルの手編集を明示的に指示したことを宣言する。generated-edit-guard がこのコマンドを見つけて" +
			"エージェントのセッションに記録し、そのセッションの間は指定したパスの編集を通す。ユーザーの明示的な指示があるときだけ実行する。\n",
	},
	idAllowNoReason: {
		EN: "hhx allow-generated-edit: --reason is required (summarize the user's instruction)",
		JA: "hhx allow-generated-edit: --reason が必要（ユーザーの指示の要旨を書く）",
	},
	idAllowNoPaths: {
		EN: "hhx allow-generated-edit: pass at least one path",
		JA: "hhx allow-generated-edit: パスを 1 つ以上指定する",
	},
	idAllowUnknownOption: {
		EN: "hhx allow-generated-edit: unknown option {{.Option}}",
		JA: "hhx allow-generated-edit: 未知のオプション {{.Option}}",
	},
	// 宣言を受け付けた。最終報告に載せる 3 点をエージェントに念押しする。
	idAllowDeclared: {
		EN: "Declared hand edits of generated files: {{.Paths}}\n" +
			"generated-edit-guard recorded this for the current agent session. In the final report, list the files, " +
			"the reason and the user's instruction, and that the files must be regenerated.\n",
		JA: "生成ファイルの手編集を宣言した: {{.Paths}}\n" +
			"generated-edit-guard が現在のエージェントのセッションに記録した。最終報告に、ファイル・理由とユーザーの指示・" +
			"作り直しが必要なことを書く。\n",
	},
	idAllowDisabled: {
		EN: "generated-edit-guard is disabled, so generated files are not blocked and no declaration is needed.\n",
		JA: "generated-edit-guard は無効なので、生成ファイルの編集は止まらず、宣言は要らない。\n",
	},

	idApplyFlag: {EN: "install the latest release", JA: "最新のリリースを入れる"},
	idDevelopment: {
		EN: "hhx {{.Version}} is a development build; update works only for release builds",
		JA: "hhx {{.Version}} は開発ビルドなので更新できない（更新はリリースのビルドだけ）",
	},
	idNoReleaseYet: {EN: "hhx {{.Version}}: no release has been published yet", JA: "hhx {{.Version}}: リリースはまだ公開されていない"},
	idUpToDate:     {EN: "hhx {{.Version}} is up to date", JA: "hhx {{.Version}} は最新"},
	idAvailable: {
		EN: "hhx {{.Latest}} is available (current: {{.Version}})\n{{.URL}}\nRun hhx update --apply to install it.\n",
		JA: "hhx {{.Latest}} が公開されている（現在: {{.Version}}）\n{{.URL}}\nhhx update --apply で入れる。\n",
	},
	idUnsupported: {
		EN: "hhx update: the release installer supports only macOS arm64",
		JA: "hhx update: リリースのインストーラーは macOS arm64 だけに対応する",
	},
	idUpdating: {EN: "Updating hhx {{.Version}} to {{.Latest}}...", JA: "hhx {{.Version}} を {{.Latest}} に更新している..."},
	// 更新した先と PATH の hhx が違うと、hook は古い版を使い続ける。
	idOtherOnPath: {
		EN: "Note: hhx on PATH is {{.OnPath}}, not the updated {{.Path}}.\n" +
			"Hooks registered with {{.OnPath}} keep running the old version.\n",
		JA: "注意: PATH 上の hhx は更新した {{.Path}} ではなく {{.OnPath}}。\n{{.OnPath}} で登録した hook は古い版のまま動く。\n",
	},

	idWaitCIUsage: {
		EN: "Usage: hhx wait-ci [reference] [options]\n\n" +
			"Report the CI result of a pull request once, after every check has finished.\n" +
			"reference is a PR number, branch or URL (default: the current branch).\n\nOptions:\n",
		JA: "使い方: hhx wait-ci [reference] [options]\n\n" +
			"PR の全 check が終わったら、CI の結果を 1 回だけ報告する。\n" +
			"reference は PR の番号・ブランチ・URL（既定は現在のブランチ）。\n\nオプション:\n",
	},
	idFlagSHA: {
		EN: "wait until this commit becomes the PR head (default: HEAD when no reference is given)",
		JA: "この commit が PR の head になるまで待つ（既定: reference を省いたときは HEAD）",
	},
	idFlagAnySHA:   {EN: "watch the first head seen, whatever its commit", JA: "最初に見えた head を、commit にかかわらず監視する"},
	idFlagInterval: {EN: "poll interval (seconds)", JA: "poll の間隔（秒）"},
	idFlagTimeout:  {EN: "overall limit (seconds)", JA: "全体の上限（秒）"},
	idFlagStart: {
		EN: "limit for the head to match and checks to appear (seconds)",
		JA: "head が一致し check が現れるまでの上限（秒）",
	},
	idFlagSettle: {EN: "how long every check must stay complete (seconds)", JA: "全 check が完了のまま続く必要のある時間（秒）"},
	idFlagNoCI: {
		EN: "with no checks after this long, look for CI in the repository (seconds)",
		JA: "この時間を過ぎても check が無ければ、repo に CI があるかを調べる（秒）",
	},
	idFlagPRLookup: {
		EN: "limit for finding the PR of a detached HEAD commit (seconds)",
		JA: "detached HEAD の commit の PR を探す上限（秒）",
	},
	idFlagProgress:  {EN: "print progress on every poll to stderr", JA: "poll のたびに進捗を stderr に出す"},
	idFlagAllChecks: {EN: "list passing checks too (default: failures only)", JA: "成功した check も並べる（既定は失敗だけ）"},

	// 以下は wait-ci の結論の行の本文。`wait-ci: ` の後ろに続く。
	idIntervalTooLow: {EN: "--interval must be 1 or more", JA: "--interval は 1 以上"},
	idToolRequired:   {EN: "{{.Tool}} is required", JA: "{{.Tool}} が必要"},
	idSkippedNoPR:    {EN: "{{.Reason}}; skipped watching", JA: "{{.Reason}}。監視をスキップした"},
	idDetachedFailed: {
		EN: "failed to resolve the PR of the detached HEAD: {{.Error}}",
		JA: "detached HEAD の PR 解決に失敗した: {{.Error}}",
	},
	idNoPRFor:      {EN: "this {{.Target}} has no PR, so watching was skipped", JA: "この {{.Target}} に PR が無いので監視をスキップした"},
	idTargetBranch: {EN: "branch", JA: "branch"},
	idTargetCommit: {EN: "commit", JA: "commit"},
	idGHKeptFailing: {
		EN: "gh calls kept failing: {{.Error}}",
		JA: "gh の呼び出しが続けて失敗した: {{.Error}}",
	},
	idUnknownHead: {EN: "unknown", JA: "不明"},
	idHeadTimeout: {
		EN: "head did not become {{.Target}} after waiting {{.Elapsed}}s (now {{.Head}})",
		JA: "{{.Elapsed}}s 待っても head が {{.Target}} にならなかった (現在 {{.Head}})",
	},
	// コンフリクト中は pull_request の workflow が起動しない。解消の手段まで示す。
	idConflictNoRun: {
		EN: "the PR conflicts with the base branch, so no check starts. Resolve it with a rebase or merge and push again",
		JA: "base ブランチとコンフリクトしていて check が 1 件も起動しない。rebase か merge で解消して push し直す",
	},
	idNoCI: {
		EN: "this repo has no CI (no checks after {{.Elapsed}}s, " +
			"and no workflow or checks on recently merged PRs); skipped watching",
		JA: "この repo には CI が無い (check が 0 件のまま {{.Elapsed}}s 経ち、" +
			"workflow も直近の merged PR の check も見つからない)。監視をスキップした",
	},
	// main への直接 push は PR の check を持たないので、PR の監視を始めない。
	idMainNoPR: {
		EN: "a direct push to main has no PR checks to watch; skipped watching",
		JA: "main への直接 push には監視する PR の CI が無い。監視をスキップした",
	},
	idEmptyTimeout: {
		EN: "no check was registered after waiting {{.Elapsed}}s",
		JA: "{{.Elapsed}}s 待っても check が 1 件も登録されなかった",
	},
	idTimeout: {
		EN: "did not finish within {{.Elapsed}}s (pending: {{.Pending}})",
		JA: "{{.Elapsed}}s 以内に完了しなかった (pending: {{.Pending}})",
	},
	idFailed:      {EN: "{{.Failed}}/{{.Total}} failed", JA: "{{.Failed}}/{{.Total}} 件が失敗"},
	idAllPassed:   {EN: "all {{.Total}} passed", JA: "全 {{.Total}} 件が成功"},
	idConflicting: {EN: "this PR conflicts with the base branch", JA: "この PR は base ブランチとコンフリクトしている"},
})

// displayLanguage は CLI の表示言語を設定から決める。設定が読めなければ英語にする（誤りは install が報告する）。
func displayLanguage() i18n.Language {
	path, err := config.DefaultPath()
	if err != nil {
		return i18n.English
	}
	cfg, err := config.Load(path)
	if err != nil {
		return i18n.English
	}
	return cfg.DisplayLanguage()
}
