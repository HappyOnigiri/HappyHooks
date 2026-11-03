// Package discardguard は、未コミットの変更を破棄する git 操作の前に、復元用の snapshot を取る hook（discard-guard）である。
//
// 方針:
//   - 破棄自体は止めない。ただし戻せない状態にはしない。一時 index で作った snapshot のコミットを、
//     reflog つきの refs/hhx/discard-snapshot に積む。追跡ファイルの変更と未追跡ファイルが入る。作業ツリーと本物の index には触らない。
//   - 対象は discardRules。git stash は stash list / stash apply で戻せるので対象外。
//   - 検出は多めに拾い、対象の特定で絞る。保存先のリポジトリは cd を出現順に辿って決める。破棄系の git ごとに
//     「その時点の作業ディレクトリ（-C があればその先）」を候補にし、候補が複数あれば全部保存する（余分に保存しても害はない）。
//     パスは先頭の ~/ と、同じコマンドの中で値の決まる変数だけを展開し、それ以外の変数や pushd / sh -c 経由のように
//     静的に追えない形は deny する。
//   - Claude Code のセッションの scratchpad（/tmp/claude-<uid>/<スラッグ>/<session_id>/scratchpad）の中の対象は保存しない。
//     そのセッションのエージェントが作った使い捨ての clone で、hook の実行時点でまだ無くてもよい（同じコマンドで clone する形）。
//   - ファイルへ書き出す here-document の本文は、同じコマンドで実行しなければ検出の対象から外す。
//   - 保存できたら無出力で抜け、作れなければ deny する。
//
// allow を返さない理由: permissionDecision はコマンド文字列全体への判定なので、複合コマンド（git reset --hard && rm -rf ...）で
// allow を返すと、同居する別のコマンドまで permission のルールを飛ばしてしまう。無出力で抜けて通常の permission の流れに任せる。
// ask を返さない理由: ask を承認しても snapshot は作られず、Codex は ask を解釈しない。
// 理由文には「ガードが働く形への書き直し」だけを書く。cp 退避のような迂回路を案内すると、snapshot なしの破棄へ誘導することになる。
//
// snapshot の寿命: 到達経路は reflog だけなので、git gc の reflog expire で自然に消える。掃除の処理は持たない。
// ref は linked worktree の間で共有される（refs/hhx/* は共通ディレクトリに置かれる）。別の作業ツリーで破棄すると ref の先頭は
// そちらに移るので、どの作業ツリーのものかはメッセージ末尾の "@ <toplevel>" で判別する（復元の手順は README）。
//
// Codex の PreToolUse には exec_command の workdir が渡らず、payload の cwd はセッション開始時のディレクトリのままである。
// この hook はその cwd を基準にする（移植元と同じ限界）。
// デバッグ経路（`hhx hook discard-guard '<コマンド>'`）では cwd が渡らないので、プロセスの作業ディレクトリを基準にする。
package discardguard

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/HappyOnigiri/hhx/internal/hookrt"
	py "github.com/HappyOnigiri/hhx/internal/pycompat"
	"github.com/HappyOnigiri/hhx/internal/version"
)

// Name は hook の名前である。
const Name = "discard-guard"

// Definition は discard-guard の定義を返す。
func Definition() hookrt.Definition {
	return hookrt.Definition{
		Name:           Name,
		DefaultEnabled: true,
		Registrations: []hookrt.Registration{
			{Agent: hookrt.Claude, Event: "PreToolUse", Matcher: "Bash"},
			{Agent: hookrt.Codex, Event: "PreToolUse", Matcher: "Bash"},
		},
		Gate: gate,
		Run:  run,
	}
}

// snapshotRef は snapshot を積む ref である。
const snapshotRef = "refs/hhx/discard-snapshot"

// snapshotIndexName は一時 index のファイル名である。作業ツリーごとの git ディレクトリに置き、削除せずに再利用する（stat cache が効く）。
const snapshotIndexName = "hhx-discard-snapshot.index"

// snapshotIdentity は snapshot のコミットの作成者である。利用者の git 設定に user.name が無くても作れるように渡す。
var snapshotIdentity = []string{
	"GIT_AUTHOR_NAME=hhx",
	"GIT_AUTHOR_EMAIL=hhx@localhost",
	"GIT_COMMITTER_NAME=hhx",
	"GIT_COMMITTER_EMAIL=hhx@localhost",
}

// hook 全体の持ち時間と、git 1 回あたりの上限。対象のリポジトリが複数あると保存も複数回走るので、
// 合計が agent 側の hook のタイムアウト（60 秒）を超えないよう、git 1 回ごとに残り時間を上限にする。
// 使い切ったらその git は失敗として扱い（deny）、agent 側のタイムアウトで無言に落ちるのを避ける。
// テストで短くするために変数にしている。
var (
	timeBudget = 50 * time.Second
	gitTimeout = 30 * time.Second
)

// gate は一次ゲートである。全 Bash 呼び出しで走るので、git を含まない入力は解析しない。
func gate(input []byte) bool {
	return bytes.Contains(input, []byte("git"))
}

func run(c *hookrt.Context) error {
	runner := gitRunner{deadline: time.Now().Add(timeBudget), failure: &gitFailure{}}
	var command, cwd, sessionID string
	var scratch scratchpad
	if c.FromArgs {
		command = string(c.Input)
	} else {
		command, cwd, sessionID, scratch = payloadFrom(c.Input)
	}
	original := command
	command, bodies := stripFileHeredocs(command)
	if command == "" || !gitCallRE.MatchString(command) {
		return nil
	}
	rules := discardRuleIDs(command)
	if len(rules) == 0 {
		return nil
	}
	label := ruleLabels(c.Language(), rules)

	resolved, err := resolveTargets(command, cwd, bodies, scratch)
	if err != nil {
		// 作業ディレクトリが得られない（移植元では例外で無出力になっていた）。
		return err
	}
	if resolved == nil {
		c.Deny(denyUnresolved(c.Language(), label))
		return nil
	}
	targets := resolved.dirs
	// scratchpad の中でも、作業ツリーが外にある（core.worktree で外を指すなど）なら保存する。
	// git 管理下でなければ破棄の git も失敗するので、保存しない。
	for _, directory := range resolved.scratch {
		if top, ok := runner.git(directory, nil, "rev-parse", "--show-toplevel"); ok && top != "" && !scratch.contains(top) {
			targets = appendNew(targets, directory)
		}
	}
	// 候補が同じ作業ツリーの別のディレクトリなら、保存は 1 回にする（toplevel で重複を除く）。
	// toplevel は --show-toplevel の値のまま比べる。正規化を変えるとメッセージの "@ <toplevel>" も変わる。
	seen := map[string]bool{}
	deny := func(target string) {
		_ = recordFailure(failureEntry{
			Time:       now().UTC().Format(time.RFC3339),
			HHXVersion: version.String(),
			SessionID:  sessionID,
			CWD:        cwd,
			Command:    clipRunes(original, maxRecordedCommand),
			Target:     target,
			gitFailure: *runner.failure,
		})
		c.Deny(denyFailed(c.Language(), label))
	}
	for _, target := range targets {
		top, ok := runner.git(target, nil, "rev-parse", "--show-toplevel")
		if !ok || top == "" {
			// 移植元との違い: 移植元は git 管理下でない対象も保存できないとして deny していた。
			// 存在するディレクトリを git 自身が「リポジトリではない」と答えたなら、破棄の git も同じ理由で失敗し、
			// 失う変更が無いので通す（grep のパターンに破棄の git の文字列を書いただけのコマンドなどが止まっていた）。
			if !ok && runner.failure.notARepository() && isDirectory(target) {
				continue
			}
			deny(target)
			return nil
		}
		if seen[top] {
			continue
		}
		seen[top] = true
		if snapshot(runner, top, strings.Join(rules, " / ")) == failed {
			deny(top)
			return nil
		}
	}
	// 保存できた、または保存するものが無かった。判定は通常の permission の流れに任せる。
	return nil
}

// payloadFrom は payload から command と cwd と、session_id のセッションの scratchpad を取り出す。型の違う値は空として扱う。
// 文字列でない command は、移植元では判定の途中で例外になり無出力で終わっていた。
func payloadFrom(raw []byte) (command, cwd, sessionID string, scratch scratchpad) {
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return "", "", "", scratchpad{}
	}
	if toolInput, ok := payload["tool_input"].(map[string]any); ok {
		command, _ = toolInput["command"].(string)
	}
	cwd, _ = payload["cwd"].(string)
	sessionID, _ = payload["session_id"].(string)
	transcriptPath, _ := payload["transcript_path"].(string)
	return command, cwd, sessionID, newScratchpad(sessionID, transcriptPath)
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// 以下の検出用のパターンは「多めに拾って、対象の特定で絞る」方針である。検出が漏れると snapshot なしで破棄が走るため、迷ったら広く拾う。
// \s と \S は Python の意味（Unicode の空白）にする。RE2 の \s は ASCII だけなので、全角空白を挟んだ形の検出が漏れる。
// Python の $ は末尾の改行の直前にも一致するが、$ を置いた位置はどれも空白（改行を含む）との選択なので、RE2 の $ でも一致は変わらない。
const (
	// gitBase は git の起動である。(git ...) や sh -c 'git ...'、パス指定の起動（/usr/bin/git ...）も拾う。
	gitBase = `(?:^|[` + py.SpaceChars + `;&|('"])(?:rtk` + py.Space + `+)?(?:` + py.NotSpace + `*/)?git`
	// optionValue はオプションの値である。\S+ だけにするとクォートの中の空白（-C 'my repo'）で切れ、サブコマンドに届かず検出が漏れる。
	optionValue = `(?:[^` + py.SpaceChars + `'"]*(?:'[^']*'|"[^"]*")|` + py.NotSpace + `+)`
	// globalOptions は git とサブコマンドの間に挟まる大域オプションである。許さないと git -C <path> reset --hard が漏れる。
	globalOptions = `(?:` + py.Space + `+(?:-[cC]` + py.Space + `*` + optionValue +
		`|-p|--paginate|--no-pager|--git-dir=` + optionValue + `|--work-tree=` + optionValue +
		`|--exec-path=` + optionValue + `|--literal-pathspecs))*`
	// gitCommand は git <大域オプション...> <サブコマンド> の、サブコマンドの直前までである。
	gitCommand = gitBase + globalOptions + py.Space + `+`
	// segment は同じコマンドの中（パイプ・コマンドの区切りを越えない）である。
	segment = `[^|;&]*`
	// end は末尾の境界である。空白と行末だけでは (git reset --hard) の閉じ括弧・閉じクォートを取り逃す。
	end = `(?:[` + py.SpaceChars + `);&|'"]|$)`
	// flagEnd はフラグの直後の境界である（--staged=x のような = も含む）。
	flagEnd = `(?:[` + py.SpaceChars + `=;&|'"]|$)`
)

var (
	gitCallRE = regexp.MustCompile(gitBase + py.Space)

	restoreRE        = regexp.MustCompile(gitCommand + `restore(` + segment + `)`)
	restoreStagedRE  = regexp.MustCompile(py.Space + `--staged` + flagEnd)
	restoreWorktree  = regexp.MustCompile(py.Space + `--worktree`)
	applyRE          = regexp.MustCompile(gitCommand + `apply(` + segment + `)`)
	applySafeFlags   = regexp.MustCompile(py.Space + `(?:--check|--stat|--summary|--numstat|--cached)` + flagEnd)
	applyDiscardFlag = regexp.MustCompile(py.Space + applyDiscardFlags + flagEnd)
)

// applyDiscardFlags の短縮形は、-p3 / -C3 の 3 を -3 と誤検出しないよう、R と 3 の組み合わせだけを並べる。
const applyDiscardFlags = `(?:--reverse|--3way|--reject|-(?:R3|3R|R|3))`

// isWorktreeRestore は git restore が作業ツリーを書き戻す形かを返す。
// パススペックを伴う restore は対象である。--staged だけの形は index の巻き戻しで作業ツリーに触らないので対象外で、
// --worktree を併記すれば対象になる。
func isWorktreeRestore(command string) bool {
	for _, match := range restoreRE.FindAllStringSubmatch(command, -1) {
		rest := match[1]
		if restoreStagedRE.MatchString(rest) && !restoreWorktree.MatchString(rest) {
			continue
		}
		for _, token := range py.Fields(rest) {
			if !strings.HasPrefix(token, "-") {
				return true
			}
		}
	}
	return false
}

// isDiscardingApply は git apply が作業ツリーの内容を破棄・上書きしうる形かを返す。
// -R はパッチが意図より広ければ必要な変更まで消し、--3way / --reject は文脈が一致しなくても書き込む。
// 既定の apply は文脈が一致しなければ何も書かずに失敗するので対象外で、--cached は index だけに触るので対象外である。
func isDiscardingApply(command string) bool {
	for _, match := range applyRE.FindAllStringSubmatch(command, -1) {
		rest := match[1]
		if applySafeFlags.MatchString(rest) {
			continue
		}
		if applyDiscardFlag.MatchString(rest) {
			return true
		}
	}
	return false
}

// discardRule は未コミットの変更を破棄する操作の 1 つである。
type discardRule struct {
	// id はルールの識別子である。snapshot のメッセージに残るので、表示言語に依存させない。理由文のラベルはカタログから引く。
	id      string
	matches func(string) bool
}

func pattern(expression string) func(string) bool {
	return regexp.MustCompile(expression).MatchString
}

// ルールの ID。snapshot のメッセージに残る永続データなので変えない。
const (
	ruleResetHard = "git-reset-hard"
	ruleCheckout  = "git-checkout"
	ruleSwitch    = "git-switch"
	ruleRestore   = "git-restore"
	ruleClean     = "git-clean"
	ruleApply     = "git-apply"
)

// discardRules は snapshot の対象になる操作である。ラベルはこの順に " / " で連結して理由文に入れる。
var discardRules = []discardRule{
	{ruleResetHard, pattern(gitCommand + `reset` + segment + py.Space + `--hard` + end)},
	// -B（ブランチの付け替え）と、パススペック・強制形（checkout . / -- <path> / -f）。
	{ruleCheckout, pattern(gitCommand + `checkout` + segment + py.Space + `(?:-B|--|\.|:/|-f|--force)` + end)},
	{ruleSwitch, pattern(gitCommand + `switch` + segment + py.Space +
		`(?:-f|--force|--discard-changes|-C)` + end)},
	{ruleRestore, isWorktreeRestore},
	// 短縮フラグは連結（-fd / -xdf / -ffd）を許すため、末尾の境界を要求しない。
	{ruleClean, pattern(gitCommand + `clean` + segment + py.Space + `(?:-[a-zA-Z]*f|--force` + end + `)`)},
	{ruleApply, isDiscardingApply},
}

// discardSubcommands は discardRules で拾うサブコマンドである。必ず一致させる（テストで縛っている）。
// 漏らすと、対象の特定が git -C <別のリポジトリ> を採用せず cwd を保存し、本来の対象が無防備になる。
var discardSubcommands = []string{"reset", "checkout", "switch", "restore", "clean", "apply"}

// discardRuleIDs は command に当たったルールの ID を、discardRules の順に返す。
func discardRuleIDs(command string) []string {
	var labels []string
	for _, rule := range discardRules {
		if rule.matches(command) {
			labels = append(labels, rule.id)
		}
	}
	return labels
}
