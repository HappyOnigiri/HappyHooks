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
	"context"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/HappyOnigiri/hhx/internal/hookrt"
	py "github.com/HappyOnigiri/hhx/internal/pycompat"
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
	runner := gitRunner{deadline: time.Now().Add(timeBudget)}
	var command, cwd string
	var scratch scratchpad
	if c.FromArgs {
		command = string(c.Input)
	} else {
		command, cwd, scratch = payloadFrom(c.Input)
	}
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
	for _, target := range targets {
		top, ok := runner.git(target, nil, "rev-parse", "--show-toplevel")
		if !ok || top == "" {
			c.Deny(denyFailed(c.Language(), label))
			return nil
		}
		if seen[top] {
			continue
		}
		seen[top] = true
		if snapshot(runner, top, strings.Join(rules, " / ")) == failed {
			c.Deny(denyFailed(c.Language(), label))
			return nil
		}
	}
	// 保存できた、または保存するものが無かった。判定は通常の permission の流れに任せる。
	return nil
}

// payloadFrom は payload から command と cwd と、session_id のセッションの scratchpad を取り出す。型の違う値は空として扱う。
// 文字列でない command は、移植元では判定の途中で例外になり無出力で終わっていた。
func payloadFrom(raw []byte) (command, cwd string, scratch scratchpad) {
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return "", "", scratchpad{}
	}
	if toolInput, ok := payload["tool_input"].(map[string]any); ok {
		command, _ = toolInput["command"].(string)
	}
	cwd, _ = payload["cwd"].(string)
	sessionID, _ := payload["session_id"].(string)
	transcriptPath, _ := payload["transcript_path"].(string)
	return command, cwd, newScratchpad(sessionID, transcriptPath)
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

var shells = []string{"sh", "bash", "zsh", "dash", "ksh"}

// unsafeEnvPrefixes は作業ツリー・git ディレクトリを環境変数で差し替える形である。これは追わない。
var unsafeEnvPrefixes = []string{"GIT_WORK_TREE=", "GIT_DIR=", "GIT_INDEX_FILE="}

// unstaticChars を含むパス（変数展開・glob・クォート・シェルのメタ文字）は静的に解決できない。
const unstaticChars = "$`*?~'\"&;|()"

func isStaticPath(path string) bool {
	return !strings.ContainsAny(path, unstaticChars)
}

func isShell(token string) bool {
	if slices.Contains(shells, token) {
		return true
	}
	index := strings.LastIndex(token, "/")
	return index >= 0 && slices.Contains(shells, token[index+1:])
}

func hasUnsafeEnvPrefix(token string) bool {
	for _, prefix := range unsafeEnvPrefixes {
		if strings.HasPrefix(token, prefix) {
			return true
		}
	}
	return false
}

// takeValue は tokens[index] から値を 1 つ取り出す。クォートが閉じていなければ、閉じるまで空白でつないで束ねる。
// 束ねないと -C 'my repo' の途中で切れてサブコマンドを取り違え、別のリポジトリを対象と誤認する。
// 戻り値は値と、最後に消費した位置である。閉じないまま尽きたら、そこまでを返す。
func takeValue(tokens []string, index int) (string, int) {
	value := tokens[index]
	for index+1 < len(tokens) && (strings.Count(value, "'")%2 == 1 || strings.Count(value, `"`)%2 == 1) {
		index++
		value += " " + tokens[index]
	}
	return value, index
}

// expandUser は Python の os.path.expanduser を、先頭が ~ か ~/ のパスに対して行う。
func expandUser(path string) string {
	home := py.Home()
	if home == "~" {
		// ホームディレクトリが得られなければ、Python と同じく展開しない。
		return path
	}
	if home == "/" {
		home = ""
	}
	if expanded := home + path[1:]; expanded != "" {
		return expanded
	}
	return "/"
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// resolution は snapshot の対象の候補である。
type resolution struct {
	// dirs は snapshot を取るディレクトリを出現順に持つ。
	dirs []string
	// scratch はセッションの scratchpad の中にある既存のディレクトリである。作業ツリーも scratchpad の中にあれば snapshot を取らない。
	scratch []string
}

func appendNew(list []string, item string) []string {
	if slices.Contains(list, item) {
		return list
	}
	return append(list, item)
}

// resolveTargetDirs は scratchpad を考えずに、snapshot の対象のディレクトリを出現順に返す。静的に特定できなければ nil を返す。
func resolveTargetDirs(command, cwd string) ([]string, error) {
	resolved, err := resolveTargets(command, cwd, nil, scratchpad{})
	if resolved == nil {
		return nil, err
	}
	return resolved.dirs, nil
}

// pathState は cd / git -C の行き先を解決した結果である。
type pathState int

const (
	unresolvedPath pathState = iota
	existingPath
	// missingScratchPath はセッションの scratchpad の中で、hook の実行時点ではまだ無いディレクトリである（同じコマンドで clone する形）。
	missingScratchPath
)

// track は模擬しているシェルの状態である。
type track struct {
	dir string
	// exists は dir が hook の実行時点で存在するかを示す。偽なら dir は scratchpad の中である。
	exists bool
	// pending は、まだ無い scratchpad のディレクトリへ移った cd のトークンの位置である（無ければ -1）。
	// その cd が実行時に失敗すると前のディレクトリに留まるので、そこから破棄までが && だけでつながっていなければ特定できないものとする。
	pending int
	// vars は値が静的に決まる変数である。無い変数は値が分からない（環境変数、条件つきの代入、$(...) の結果など）。
	vars map[string]string
}

func (t track) clone() track {
	t.vars = maps.Clone(t.vars)
	return t
}

// resolveTargets は snapshot の対象を出現順に返す。静的に特定できなければ nil を返す。
// bodies は、シェルへ流し込む here-document の本文の範囲である（ファイルへ書き出す本文は呼び出し側で取り除いてある）。
//
// cd / git -C が別のリポジトリを指しているのに cwd を見ると、無関係なリポジトリを保存して本来の対象を無防備に破棄させる。
// そこで cd を出現順に辿ってシェルの作業ディレクトリを模擬し、破棄系の git ごとに「その時点の作業ディレクトリ（-C があればその先）」を候補にする。
// cd / -C の行き先が存在しなければ特定できないものとする。存在しない cd は実行時に失敗して前のディレクトリに留まるので、
// その後の相対パス（cd ..）を取り違えると本来の対象を保存し損ねる。
// (cd dir && git ...) の括弧や make -C のような無関係な -C を正規表現の境界だけで見分けるのは無理があるので、トークン単位で走査する。
//
// 次の 2 点は移植元の Python 実装から意図して変えている。どちらも移植元では別のリポジトリを保存して通し、本来の対象の変更を失っていた。
//   - ( ... ) と $( ... ) はサブシェルなので、中の cd を閉じ括弧で取り消す。移植元は括弧を空白として読み、閉じた後も cd を残していた。
//     ただし括弧はクォートや case の分岐を見分けずに数えるので、実際には閉じていない括弧で cd を取り消しうる。
//     そこで括弧を無視した作業ディレクトリ（移植元の意味）も並行して辿り、両方を候補にする。移植元より保存先が減る入力は無い。
//   - 1 つの git に -C が複数あれば、git と同じく出現順に適用し、相対パスは直前の -C の先から辿る。移植元は最後の -C だけを cwd から解決していた。
//
// 移植元に無い機能として、同じコマンドの中で値の決まる変数（S=/abs; D=$S/x）を cd / git -C のパスとして展開し、
// セッションの scratchpad の中の対象は snapshot の候補から外す（使い捨ての clone での破棄を止めないため）。
// 変数は、その名前がコマンドの中で 1 回だけ、条件なしの単独の代入として現れる場合だけ信用する（trustedNames）。
func resolveTargets(command, cwd string, bodies []span, scratch scratchpad) (*resolution, error) {
	base := cwd
	if base == "" {
		var err error
		if base, err = py.Getcwd(); err != nil {
			return nil, err
		}
	}
	if !isDir(base) {
		return nil, nil
	}
	r := newResolver(command, base, bodies, scratch)
	for index := range r.tokens {
		if !r.step(index) {
			return nil, nil
		}
	}
	// 破棄系の git をトークンとして拾えなかった（クォートの中など）が、discardRules には当たっている。
	// 多めに拾う方針なので、最後の作業ディレクトリを保存しておく。
	if !r.found {
		for _, t := range []track{r.current, r.flat} {
			if t.pending >= 0 || !r.candidate(t, t.dir, t.exists, true, len(r.tokens)-1) {
				return nil, nil
			}
		}
	}
	return &r.result, nil
}

// resolver は resolveTargets の走査の状態である。
type resolver struct {
	tokens  []token
	words   []string
	bodies  []span
	scratch scratchpad
	result  resolution
	found   bool

	// current は括弧をサブシェルとして辿り、flat は括弧を無視して辿る（閉じ括弧で cd も変数も取り消さない）。
	current, flat track
	// outer はサブシェルに入る前の状態を積む。対応の無い閉じ括弧（case の分岐など）は無視する。
	outer []track

	// trusted は値の変わらない変数の名前である（trustedNames）。
	trusted map[string]bool
	// linked は ln を含むかを示す。同じコマンドで symlink を作れば、scratchpad の中のまだ無いパスが外のリポジトリを指しうる。
	linked bool
	// varsDisabled は eval / source を過ぎたかを示す。これらは任意の変数を書き換えうるので、以降は変数を展開しない。
	varsDisabled bool
	// errexit は set -e が効いているかを示す。効いていれば、単独の cd の失敗でシェルが終わる。
	errexit bool
	// blocks は if / for などの複合コマンドと { ... } の深さである。中の代入と cd は条件つきでしか実行されない。
	blocks int
	// assigning は、直前のトークンがコマンドの位置から続く代入（または export）であることを示す。runStart はその先頭の位置である。
	assigning bool
	runStart  int
}

func newResolver(command, base string, bodies []span, scratch scratchpad) *resolver {
	tokens := tokenize(command)
	words := make([]string, len(tokens))
	for index, token := range tokens {
		words[index] = token.text
	}
	current := track{dir: base, exists: true, pending: -1, vars: map[string]string{}}
	return &resolver{
		tokens:  tokens,
		words:   words,
		bodies:  bodies,
		scratch: scratch,
		current: current,
		flat:    current.clone(),
		trusted: trustedNames(command),
		linked:  slices.ContainsFunc(words, func(word string) bool { return word == "ln" || strings.HasSuffix(word, "/ln") }),
	}
}

// step は tokens[index] を読んで状態を進める。対象を特定できなければ偽を返す。
func (r *resolver) step(index int) bool {
	token := r.tokens[index]
	r.enterOrLeave(token.seps)
	word := token.text
	atCommand := index == 0 || token.seps != "" || slices.Contains(keywordsBeforeCommand, r.words[index-1])
	next := ""
	if index+1 < len(r.words) {
		next = r.words[index+1]
	}

	if hasUnsafeEnvPrefix(word) {
		return false
	}
	if name, value, ok := assignment(word); ok && (atCommand || r.assigning) {
		r.assign(index, name, value)
		return true
	}
	r.assigning = atCommand && word == "export"
	if r.assigning {
		r.runStart = index
		return true
	}

	switch {
	case atCommand && slices.Contains(blockOpeners, word):
		r.blocks++
	case atCommand && slices.Contains(blockClosers, word):
		r.blocks = max(r.blocks-1, 0)
	case word == "eval" || word == "source" || (atCommand && word == "."):
		r.varsDisabled = true
	case atCommand && word == "set" && !r.inBody(index) && r.blocks == 0 && len(r.outer) == 0:
		if on, ok := errexitFlag(r.words, index+1); ok {
			r.errexit = on
		}
	case word == "cd" || word == "pushd" || word == "popd":
		// pushd / popd は行き先を追えない。引数の無い cd（ホームへ移る）と cd - も追わない。
		return word == "cd" && next != "" && !strings.HasPrefix(next, "-") && r.cd(index, atCommand)
	case isShell(word):
		// 別のシェル経由（sh -c '...' / bash -lc '...'）は、中身をトークンとして追えない。
		return !strings.HasPrefix(next, "-")
	case word == "git" || strings.HasSuffix(word, "/git"):
		return r.git(index)
	}
	return true
}

// enterOrLeave は語の前の区切りにある括弧でサブシェルに出入りし、波括弧の深さを数える。
func (r *resolver) enterOrLeave(seps string) {
	for _, separator := range []byte(seps) {
		switch separator {
		case '(':
			r.outer = append(r.outer, r.current.clone())
		case ')':
			if len(r.outer) > 0 {
				r.current = r.outer[len(r.outer)-1]
				r.outer = r.outer[:len(r.outer)-1]
			}
		case '{':
			r.blocks++
		case '}':
			r.blocks = max(r.blocks-1, 0)
		}
	}
}

func (r *resolver) inBody(index int) bool {
	return inSpans(r.bodies, r.tokens[index].offset)
}

// vars は index の語の展開に使う変数を返す。シェルへ流し込む本文の中（外側の変数は見えない）と eval / source の後は nil を返す。
func (r *resolver) vars(t track, index int) map[string]string {
	if r.inBody(index) || r.varsDisabled {
		return nil
	}
	return t.vars
}

// assign は NAME=VALUE の代入を読む。条件なしに単独で走り、名前が 1 回しか現れない代入だけを変数の値にし、それ以外は値を分からなくする。
func (r *resolver) assign(index int, name, value string) {
	if !r.assigning {
		r.runStart = index
	}
	r.assigning = true
	valid := !r.inBody(index) && !r.varsDisabled && r.blocks == 0 && r.trusted[name] &&
		!strings.ContainsAny(r.tokens[r.runStart].seps, "&|") && assignmentStandsAlone(r.tokens, index)
	for _, t := range []*track{&r.current, &r.flat} {
		if valid {
			if expanded, ok := expandWord(value, t.vars, true); ok {
				t.vars[name] = expanded
				continue
			}
		}
		delete(t.vars, name)
	}
}

// cd は tokens[index] の cd で作業ディレクトリを移す。行き先を特定できなければ偽を返す。
func (r *resolver) cd(index int, atCommand bool) bool {
	path, last := takeValue(r.words, index+1)
	// set -e の下で条件なしに単独で走る cd は、失敗すればシェルごと終わる。
	standalone := r.errexit && atCommand && r.blocks == 0 && !strings.ContainsAny(r.tokens[index].seps, "&|") &&
		(last+1 >= len(r.tokens) || r.tokens[last+1].seps != "" && !strings.ContainsAny(r.tokens[last+1].seps, "&|"))
	for _, t := range []*track{&r.current, &r.flat} {
		destination, state, absolute := r.resolve(t.dir, r.vars(*t, index), path)
		if state == unresolvedPath {
			return false
		}
		t.dir, t.exists = destination, state == existingPath
		switch {
		case t.exists:
			t.pending = -1
		case absolute || t.pending < 0:
			// 絶対パスの cd は前の作業ディレクトリに依らないので、この cd から判定し直す。
			// 相対パスなら、前の cd が失敗していれば行き先も変わるので、最初の cd から判定する。
			t.pending = index
			if standalone {
				t.pending = -1
			}
		}
	}
	return true
}

// git は tokens[index] の git が破棄系なら、走るディレクトリを候補に足す。特定できなければ偽を返す。
func (r *resolver) git(index int) bool {
	directories, subcommand, ok := parseGitOptions(r.words, index+1)
	if !ok {
		return false
	}
	// -C は破棄系のサブコマンドに付いていた場合だけ採用する（git -C <other> fetch && git reset --hard に乗っ取られないため）。
	if !slices.Contains(discardSubcommands, subcommand) {
		return true
	}
	r.found = true
	for _, t := range []track{r.current, r.flat} {
		vars := r.vars(t, index)
		target, exists, dependsOnBase := t.dir, t.exists, true
		for _, directory := range directories {
			resolved, state, absolute := r.resolve(target, vars, directory)
			if state == unresolvedPath {
				return false
			}
			// 絶対パスの -C は、それより前の作業ディレクトリ（失敗したかもしれない cd の結果）に依らない。
			dependsOnBase = dependsOnBase && !absolute
			target, exists = resolved, state == existingPath
		}
		if !r.candidate(t, target, exists, dependsOnBase, index) {
			return false
		}
	}
	return true
}

// resolve は from を基準に word を解決する。先頭の ~ / ~/ だけは展開する（hook の HOME はシェルと同じなので決まる）。
// クォートつきの "~/x" はシェルも展開しない。~user の形は追わない。3 つ目の戻り値は word が絶対パスだったかを示す。
func (r *resolver) resolve(from string, vars map[string]string, word string) (string, pathState, bool) {
	// 語が空白を含むのは、クォートの中の空白や区切り文字で分けた断片を takeValue が 1 つの空白でつないだときだけである。
	// つないだ値は元のパスと違いうる（'a  b'、"a;b"、全角空白）ので追わない。
	if strings.ContainsFunc(word, py.IsSpace) {
		return "", unresolvedPath, false
	}
	path, ok := expandPath(word, vars)
	if !ok {
		return "", unresolvedPath, false
	}
	absolute := strings.HasPrefix(path, "/")
	full := path
	if !absolute {
		full = py.Join(from, path)
	}
	if isDir(full) {
		return full, existingPath, absolute
	}
	if !r.linked && r.scratch.contains(full) {
		return full, missingScratchPath, absolute
	}
	return "", unresolvedPath, absolute
}

// chainSafe は、from の cd から to の git までが && だけでつながっているかを返す（cd が失敗すれば git まで届かない）。
func (r *resolver) chainSafe(from, to int) bool {
	if strings.ContainsRune(r.tokens[from].seps, '|') {
		return false
	}
	for index := from + 1; index <= to; index++ {
		if !onlyAndThen(r.tokens[index].seps) {
			return false
		}
	}
	return true
}

// candidate は、破棄が t の状態で target に対して走るときの候補を足す。特定できなければ偽を返す。
func (r *resolver) candidate(t track, target string, exists, dependsOnBase bool, at int) bool {
	if dependsOnBase && t.pending >= 0 && !r.chainSafe(t.pending, at) {
		return false
	}
	switch {
	case !exists:
		// scratchpad の中のまだ無いディレクトリ。git -C は失敗し、cd は失敗すれば破棄まで届かない。
	case r.scratch.contains(target):
		r.result.scratch = appendNew(r.result.scratch, target)
	default:
		r.result.dirs = appendNew(r.result.dirs, target)
	}
	return true
}

// token は command の 1 語である。
type token struct {
	text string
	// seps は直前の語との間にある区切り文字（`(){};&|` と改行）を出現順に持つ。サブシェルの出入りと、コマンドの境界を辿るために使う。
	seps string
	// offset は command の中での語の先頭の位置である（here-document の本文の中かを判定する）。
	offset int
}

// tokenize は区切り文字を空白に寄せて command をトークンに分ける。区切りと語がくっついた形でも cd / git をトークンとして拾うためである。
// トークンの列は、括弧を空白として読んだ移植元と同じにする（区切りそのものはトークンにしない）。
// 例外として、${NAME} の波括弧は区切りにせず語の一部にする（変数の参照として展開するため）。名前だけを囲む形に限るので、
// ${X:-$(cd ...)} のような中身を 1 語に隠すことはない。
func tokenize(command string) []token {
	var tokens []token
	var seps []byte
	start := -1
	flush := func(end int) {
		if start >= 0 {
			tokens = append(tokens, token{text: command[start:end], seps: string(seps), offset: start})
			seps, start = nil, -1
		}
	}
	for index := 0; index < len(command); {
		r, size := utf8.DecodeRuneInString(command[index:])
		switch {
		case r == '$' && strings.HasPrefix(command[index+1:], "{"):
			if length := bracedNameLength(command[index+2:]); length > 0 {
				if start < 0 {
					start = index
				}
				index += 2 + length + 1
				continue
			}
			if start < 0 {
				start = index
			}
		case strings.ContainsRune(separatorChars, r):
			flush(index)
			seps = append(seps, byte(r))
		case py.IsSpace(r):
			flush(index)
			if r == '\n' {
				seps = append(seps, '\n')
			}
		default:
			if start < 0 {
				start = index
			}
		}
		index += size
	}
	flush(len(command))
	return tokens
}

// separatorChars は tokenize で空白として扱う区切り文字である。
const separatorChars = "(){};&|"

// bracedNameLength は s が NAME} で始まるとき NAME の長さを返し、そうでなければ 0 を返す。
func bracedNameLength(s string) int {
	length := nameLength(s)
	if length == 0 || !strings.HasPrefix(s[length:], "}") {
		return 0
	}
	return length
}

// nameLength は s の先頭にあるシェルの変数名の長さを返す（無ければ 0）。
func nameLength(s string) int {
	for index := 0; index < len(s); index++ {
		c := s[index]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || index > 0 && c >= '0' && c <= '9' {
			continue
		}
		return index
	}
	return len(s)
}

func isNameChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// onlyAndThen は区切りが && だけ（括弧・波括弧と && の後の改行を除く）か、区切りが無い（同じコマンドの中）かを返す。
func onlyAndThen(seps string) bool {
	rest := strings.Map(func(r rune) rune {
		if strings.ContainsRune("(){}", r) {
			return -1
		}
		return r
	}, seps)
	return rest == "" || strings.HasPrefix(rest, "&&") && strings.Trim(rest[2:], "\n") == ""
}

// keywordsBeforeCommand の直後の語はコマンドの位置にある（then D=x の D=x は代入として読む）。
var keywordsBeforeCommand = []string{"then", "do", "else", "elif", "if", "while", "until", "!"}

// blockOpeners と blockClosers は、中のコマンドが条件つきでしか実行されない複合コマンドの開始と終了である。
var (
	blockOpeners = []string{"if", "case", "for", "select", "while", "until", "function"}
	blockClosers = []string{"fi", "esac", "done"}
)

// assignment は NAME=VALUE の形の語を分ける。
func assignment(word string) (name, value string, ok bool) {
	length := nameLength(word)
	if length == 0 || !strings.HasPrefix(word[length:], "=") {
		return "", "", false
	}
	return word[:length], word[length+1:], true
}

// assignmentStandsAlone は、tokens[index] から続く代入の並びの後にコマンドが続かず（D=x git ... は git の環境だけを変える）、
// パイプやバックグラウンド（サブシェルで走るので、シェルの変数を変えない）で終わらないかを返す。
func assignmentStandsAlone(tokens []token, index int) bool {
	next := index + 1
	for next < len(tokens) && tokens[next].seps == "" {
		if _, _, ok := assignment(tokens[next].text); !ok {
			return false
		}
		next++
	}
	return next >= len(tokens) || !hasLonePipeOrAmpersand(tokens[next].seps)
}

// hasLonePipeOrAmpersand は区切りが && / || ではない | か & を含むかを返す。
func hasLonePipeOrAmpersand(seps string) bool {
	for index := 0; index < len(seps); index++ {
		c := seps[index]
		if c != '&' && c != '|' {
			continue
		}
		if index+1 < len(seps) && seps[index+1] == c {
			index++
			continue
		}
		return true
	}
	return false
}

// errexitFlag は set の引数 words[start:] から errexit（-e / -o errexit、-euo pipefail のような連結）の指定を読む。
// ok が偽なら指定が無い。
func errexitFlag(words []string, start int) (on, ok bool) {
	for index := start; index < len(words); index++ {
		word := words[index]
		if len(word) < 2 || word[0] != '-' && word[0] != '+' || word == "--" || nameLength(word[1:]) != len(word)-1 {
			break
		}
		enable := word[0] == '-'
		if strings.ContainsRune(word[1:], 'e') {
			on, ok = enable, true
		}
		if strings.ContainsRune(word[1:], 'o') && index+1 < len(words) {
			// -o の値（オプションの名前）を読む。
			index++
			if words[index] == "errexit" {
				on, ok = enable, true
			}
		}
	}
	return on, ok
}

// trustedNames は、コマンドの中で 1 回だけ名前として現れる変数を返す。$NAME / ${NAME} の参照は数えない。
// 代入の他に名前が現れれば（for D in、read D、unset D、D+=x、2 回目の代入、${D:=x} など）値が変わりうるので信用しない。
// クォートの中やパスの一部でも数えるので多めに数えるが、多めに数えても特定できないものが増えるだけである。
func trustedNames(command string) map[string]bool {
	counts := map[string]int{}
	for index := 0; index < len(command); {
		length := nameLength(command[index:])
		if length == 0 || index > 0 && isNameChar(command[index-1]) {
			index++
			continue
		}
		name := command[index : index+length]
		end := index + length
		before, after := byte(' '), byte(' ')
		if index > 0 {
			before = command[index-1]
		}
		if end < len(command) {
			after = command[end]
		}
		switch {
		case before == '{' && index >= 2 && command[index-2] == '$':
			// ${NAME} は参照である。${NAME:=x} のように後ろが続く形は代入しうるので数える。
			if after != '}' {
				counts[name]++
			}
		case before == '$' || strings.IndexByte(notMentionBefore, before) >= 0:
		case strings.IndexByte(notMentionAfter, after) >= 0:
		default:
			counts[name]++
		}
		index = end
	}
	trusted := map[string]bool{}
	for name, count := range counts {
		trusted[name] = count == 1
	}
	return trusted
}

// notMentionBefore と notMentionAfter は、直前・直後にあれば変数への書き込みになりえない文字である（パスやフラグの一部）。
const (
	notMentionBefore = "/.-:=@%+,^#~"
	notMentionAfter  = "/.:,@#~{"
)

// expandPath は cd / git -C の引数を展開する。展開できなければ ok は偽になる。
// 先頭の ~ / ~/ だけのパスは移植元と同じく展開し、変数やクォートを含むものは vars（nil なら展開しない）で expandWord する。
func expandPath(word string, vars map[string]string) (string, bool) {
	path := word
	if path == "~" || strings.HasPrefix(path, "~/") {
		path = expandUser(path)
	}
	if isStaticPath(path) {
		return path, true
	}
	if vars == nil {
		return "", false
	}
	return expandWord(word, vars, false)
}

// expandWord は語の中の $NAME / ${NAME} を vars の値で、クォートを中身で置き換える。展開できなければ ok は偽になる。
// クォートの外の値は、シェルが空白で分割し glob を展開するので、空白や glob の文字を含めば展開できないものとする（代入の右辺は分割しない）。
// 空の値も展開できないものとする（cd $EMPTY はホームへ移り、git -C "" は今のディレクトリを指す）。
// 先頭の ~ / ~/ はシェルと同じく展開する。バッククォート・$(...)・エスケープ・特殊変数は追わない。
func expandWord(word string, vars map[string]string, assigned bool) (string, bool) {
	var builder strings.Builder
	index := 0
	if word == "~" || strings.HasPrefix(word, "~/") {
		home := expandUser(word[:min(len(word), 2)])
		if strings.HasPrefix(home, "~") {
			return "", false
		}
		builder.WriteString(home)
		index = min(len(word), 2)
	}
	reference := func(quoted bool) bool {
		name := word[index+1:]
		length := nameLength(name)
		consumed := 1 + length
		if strings.HasPrefix(name, "{") {
			length = bracedNameLength(name[1:])
			name, consumed = name[1:], 3+length
		}
		if length == 0 {
			return false
		}
		value, ok := vars[name[:length]]
		if !ok || value == "" || !quoted && !assigned && strings.ContainsFunc(value, splitsOrGlobs) {
			return false
		}
		builder.WriteString(value)
		index += consumed
		return true
	}
	for index < len(word) {
		switch c := word[index]; c {
		case '\'':
			end := strings.IndexByte(word[index+1:], '\'')
			if end < 0 {
				return "", false
			}
			builder.WriteString(word[index+1 : index+1+end])
			index += end + 2
		case '"':
			index++
			for {
				if index >= len(word) {
					return "", false
				}
				c := word[index]
				if c == '"' {
					index++
					break
				}
				if c == '$' {
					if !reference(true) {
						return "", false
					}
					continue
				}
				if c == '`' || c == '\\' {
					return "", false
				}
				builder.WriteByte(c)
				index++
			}
		case '$':
			if !reference(false) {
				return "", false
			}
		default:
			if strings.IndexByte(unquotedUnsupported, c) >= 0 {
				return "", false
			}
			builder.WriteByte(c)
			index++
		}
	}
	return builder.String(), true
}

// unquotedUnsupported はクォートの外にあれば展開を追わない文字である（glob・ブレース展開・エスケープ・リダイレクトなど）。
const unquotedUnsupported = "`*?[]{}\\~&;|()<>! \t\n"

func splitsOrGlobs(r rune) bool {
	return py.IsSpace(r) || strings.ContainsRune("*?[", r)
}

// --- here-document ---------------------------------------------------------

// span は command の中の範囲 [start, end) である。
type span struct{ start, end int }

func inSpans(spans []span, offset int) bool {
	return slices.ContainsFunc(spans, func(s span) bool { return s.start <= offset && offset < s.end })
}

// heredoc は 1 行の中の here-document の演算子である。
type heredoc struct {
	delimiter string
	// quoted は区切りの語がクォートされているか（本文を展開しない）を示す。
	quoted bool
	// stripTabs は <<- の形（本文と区切りの行の先頭のタブを除く）を示す。
	stripTabs bool
	// files は、本文をファイルへ書き出すだけの形（cat > file / tee file）で書き出すファイルの名前（パスの最後の要素）である。
	// それ以外の形では空である。
	files []string
}

// stripFileHeredocs は、ファイルへ書き出す here-document の本文（cat > x.sh <<'EOF' ... EOF）を取り除いた command と、
// 残した本文（シェルへ流し込む bash <<'EOF' など）の範囲を返す。書き出した本文はこのコマンドの中では実行されないので、
// 破棄の検出と対象の特定から外す。別の呼び出しでそのスクリプトを実行しても hook には中身が見えないので、書き出しを検出しても保護は強くならない。
// 次の本文は取り除かない（このコマンドの中で実行されうる）。
//   - 書き出したファイルの名前が、本文の外にもう一度現れるもの（cat > run.sh <<'EOF' ... EOF; ./run.sh）。
//   - 区切りがクォートされておらず、$(...) やバッククォートを含むもの（外側のシェルが本文の中で実行する）。
func stripFileHeredocs(command string) (string, []span) {
	if !strings.Contains(command, "<<") {
		return command, nil
	}
	pieces := splitHeredocs(command)
	var outside strings.Builder
	for _, piece := range pieces {
		if !piece.body {
			outside.WriteString(piece.text)
		}
	}
	var builder strings.Builder
	var bodies []span
	for _, piece := range pieces {
		if piece.body {
			operator := piece.operator
			removable := len(operator.files) > 0 &&
				(operator.quoted || !strings.Contains(piece.text, "$(") && !strings.Contains(piece.text, "`")) &&
				!slices.ContainsFunc(operator.files, func(name string) bool { return strings.Count(outside.String(), name) > 1 })
			if removable {
				continue
			}
			start := builder.Len()
			builder.WriteString(piece.text)
			bodies = append(bodies, span{start, builder.Len()})
			continue
		}
		builder.WriteString(piece.text)
	}
	return builder.String(), bodies
}

// heredocPiece は command を here-document の本文とそれ以外に分けた断片である。
type heredocPiece struct {
	text     string
	body     bool
	operator heredoc
}

// splitHeredocs は command を here-document の本文とそれ以外に分ける。本文は区切りの行の直前までで、区切りの行は本文の外に置く。
// 区切りの行が無ければ末尾までを本文とする（bash と同じ）。
func splitHeredocs(command string) []heredocPiece {
	var pieces []heredocPiece
	var quote byte
	var outside strings.Builder
	rest := command
	for rest != "" {
		line, tail, more := strings.Cut(rest, "\n")
		operators := heredocsInLine(line, &quote)
		outside.WriteString(line)
		if !more {
			break
		}
		outside.WriteByte('\n')
		rest = tail
		for _, operator := range operators {
			bodyEnd, after := len(rest), len(rest)
			for position := 0; position < len(rest); {
				candidate, _, hasNext := strings.Cut(rest[position:], "\n")
				compared := candidate
				if operator.stripTabs {
					compared = strings.TrimLeft(candidate, "\t")
				}
				next := position + len(candidate)
				if hasNext {
					next++
				}
				if compared == operator.delimiter {
					bodyEnd, after = position, next
					break
				}
				position = next
			}
			pieces = append(pieces, heredocPiece{text: outside.String()})
			outside.Reset()
			pieces = append(pieces, heredocPiece{text: rest[:bodyEnd], body: true, operator: operator})
			outside.WriteString(rest[bodyEnd:after])
			rest = rest[after:]
		}
	}
	return append(pieces, heredocPiece{text: outside.String()})
}

// heredocsInLine は line の中の here-document の演算子を出現順に返す。quote は行をまたぐクォートの状態である。
func heredocsInLine(line string, quote *byte) []heredoc {
	var operators []heredoc
	// segment は今の単純コマンドの開始位置と、その中の演算子の添字である。
	segmentStart, firstOperator := 0, 0
	finish := func(end int) {
		files := writtenFiles(line, segmentStart, end)
		for index := firstOperator; index < len(operators); index++ {
			operators[index].files = files
		}
		firstOperator = len(operators)
	}
	for index := 0; index < len(line); index++ {
		c := line[index]
		if *quote != 0 {
			if *quote == '"' && c == '\\' {
				index++
			} else if c == *quote {
				*quote = 0
			}
			continue
		}
		switch {
		case c == '\\':
			index++
		case c == '\'' || c == '"':
			*quote = c
		case c == '#' && (index == 0 || py.IsSpace(rune(line[index-1]))):
			// コメントの中の << は演算子ではない。
			finish(index)
			return operators
		case strings.IndexByte(separatorChars+"`", c) >= 0:
			finish(index)
			segmentStart = index + 1
		case c == '<' && strings.HasPrefix(line[index:], "<<") && !strings.HasPrefix(line[index:], "<<<") &&
			(index == 0 || line[index-1] != '<'):
			operator, end, ok := parseHeredoc(line, index+2)
			if ok {
				operators = append(operators, operator)
			}
			index = end - 1
		}
	}
	finish(len(line))
	return operators
}

// parseHeredoc は line[start:]（<< の直後）から区切りの語を読む。end は読み終えた位置である。
func parseHeredoc(line string, start int) (operator heredoc, end int, ok bool) {
	index := start
	if index < len(line) && line[index] == '-' {
		operator.stripTabs = true
		index++
	}
	for index < len(line) && (line[index] == ' ' || line[index] == '\t') {
		index++
	}
	var delimiter strings.Builder
	for index < len(line) {
		c := line[index]
		switch {
		case c == '\'' || c == '"':
			closing := strings.IndexByte(line[index+1:], c)
			if closing < 0 {
				return heredoc{}, len(line), false
			}
			delimiter.WriteString(line[index+1 : index+1+closing])
			operator.quoted = true
			index += closing + 2
			continue
		case c == '\\':
			operator.quoted = true
			if index+1 < len(line) {
				delimiter.WriteByte(line[index+1])
			}
			index += 2
			continue
		case py.IsSpace(rune(c)) || strings.IndexByte(separatorChars+"<>`", c) >= 0:
		default:
			delimiter.WriteByte(c)
			index++
			continue
		}
		break
	}
	operator.delimiter = delimiter.String()
	return operator, min(index, len(line)), operator.delimiter != ""
}

// writtenFiles は line[start:end] の単純コマンドが here-document の本文をファイルへ書き出すだけなら、書き出すファイルの名前を返す。
// cat はファイルへのリダイレクトの先、tee は引数のファイルを書き出し先とみなす。パイプで次のコマンドへ渡す形、
// コマンド置換（$( ... ) / バッククォート）の中、プロセス置換（>( ... )）は、本文が実行されうるので対象外（nil）にする。
func writtenFiles(line string, start, end int) []string {
	if end < len(line) && line[end] == '|' && !strings.HasPrefix(line[end:], "||") {
		return nil
	}
	if start > 0 && line[start-1] == '`' {
		return nil
	}
	if start > 1 && line[start-1] == '(' && strings.IndexByte("$<>", line[start-2]) >= 0 {
		return nil
	}
	segment := line[start:end]
	if strings.Contains(segment, ">(") || strings.Contains(segment, "<(") {
		return nil
	}
	words := py.Fields(segment)
	for len(words) > 0 {
		if _, _, ok := assignment(words[0]); !ok {
			break
		}
		words = words[1:]
	}
	if len(words) == 0 {
		return nil
	}
	var paths []string
	switch words[0] {
	case "tee":
		for _, word := range words[1:] {
			if !strings.HasPrefix(word, "-") && !strings.HasPrefix(word, "<") && !strings.HasPrefix(word, ">") {
				paths = append(paths, word)
			}
		}
	case "cat":
		for index := strings.IndexByte(segment, '>'); index >= 0; {
			target := py.Fields(strings.TrimLeft(segment[index+1:], ">"))
			if len(target) > 0 && !strings.HasPrefix(target[0], "&") {
				paths = append(paths, target[0])
			}
			next := strings.IndexByte(segment[index+1:], '>')
			if next < 0 {
				break
			}
			index += 1 + next
		}
	}
	var files []string
	for _, path := range paths {
		name := strings.Trim(path[strings.LastIndex(path, "/")+1:], `'"`)
		if name == "" || strings.ContainsAny(name, "<>") {
			// ファイルの名前が分からなければ、実行されるかを確かめられない。
			return nil
		}
		files = append(files, name)
	}
	return files
}

// --- scratchpad ------------------------------------------------------------

// scratchRoot は Claude Code が scratchpad を置く一時ディレクトリの根である（TMPDIR に依らず /tmp/claude-<uid> の下に作る）。
// テストで差し替えるために変数にしている。
var scratchRoot = "/tmp"

// scratchpad はセッションの scratchpad（<scratchRoot>/claude-<uid>/<cwd のスラッグ>/<session_id>/scratchpad）である。
// 零値はどのパスも含まない。
type scratchpad struct {
	session string
}

// sessionIDRE は scratchpad の判定に使う session_id の形である。パスの区切りや . / .. を含む値は使わない。
var sessionIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// newScratchpad は payload の session_id のセッションの scratchpad を返す。判定は Claude Code の scratchpad のパスの形で行うので、
// Codex のセッションでは零値にする（Codex の作業用ディレクトリの形は確かめていない）。
func newScratchpad(sessionID, transcriptPath string) scratchpad {
	if !sessionIDRE.MatchString(sessionID) || isCodex(sessionID, transcriptPath) {
		return scratchpad{}
	}
	return scratchpad{session: sessionID}
}

// isCodex は transcript の名前（rollout-*-<session_id>.jsonl）で Codex かを判定する。環境変数は親の CLI の値を継承し得るため使わない。
func isCodex(sessionID, transcriptPath string) bool {
	name := transcriptPath[strings.LastIndex(transcriptPath, "/")+1:]
	return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, "-"+sessionID+".jsonl")
}

// contains は path が scratchpad の中かを返す。symlink（macOS の /tmp → /private/tmp を含む）を解決してから比べるので、
// scratchpad の中の symlink が外のリポジトリを指していれば外として扱う。<cwd のスラッグ> は 1 階層の任意の名前として扱う。
func (s scratchpad) contains(path string) bool {
	if s.session == "" {
		return false
	}
	root, err := py.Realpath(scratchRoot)
	if err != nil {
		return false
	}
	resolved, err := py.Realpath(path)
	if err != nil {
		return false
	}
	rest, ok := strings.CutPrefix(resolved, strings.TrimSuffix(root, "/")+"/claude-"+strconv.Itoa(os.Getuid())+"/")
	if !ok {
		return false
	}
	parts := strings.SplitN(rest, "/", 4)
	return len(parts) >= 3 && parts[0] != "" && parts[1] == s.session && parts[2] == "scratchpad"
}

// parseGitOptions は tokens[start:] を git の大域オプションとして読み、-C の値を出現順に並べたものとサブコマンドを返す。
// サブコマンドが無ければ空文字列を返す。--git-dir / --work-tree と、値の無い -C は特定できないものとして ok を偽にする。
func parseGitOptions(tokens []string, start int) (directories []string, subcommand string, ok bool) {
	count := len(tokens)
	index := start
	for ; index < count; index++ {
		option := tokens[index]
		switch {
		case option == "-C":
			if index+1 >= count {
				return nil, "", false
			}
			var directory string
			directory, index = takeValue(tokens, index+1)
			directories = append(directories, directory)
		case strings.HasPrefix(option, "-C"):
			var value string
			value, index = takeValue(tokens, index)
			directories = append(directories, value[2:])
		case strings.HasPrefix(option, "--git-dir") || strings.HasPrefix(option, "--work-tree"):
			// 作業ツリーが cwd から外れるが、対象の特定までは踏み込まない。
			return nil, "", false
		case option == "-c":
			if index+1 < count {
				// -c k=v の値を読み飛ばす。
				_, index = takeValue(tokens, index+1)
			}
		case strings.HasPrefix(option, "-"):
		default:
			return directories, option, true
		}
	}
	return directories, "", true
}

// snapshotResult は snapshot の結果である。
type snapshotResult int

const (
	saved snapshotResult = iota
	// clean は保存するものが無かった（作業ツリーが HEAD と同じ）ことを示す。
	clean
	failed
)

// snapshot は作業ツリー top の未コミットの変更を snapshot のコミットにして snapshotRef に積む。
// top は rev-parse --show-toplevel の値で、rules は発火したルールの ID を " / " で連結したものである。
func snapshot(runner gitRunner, top, rules string) snapshotResult {
	gitDir, ok := runner.git(top, nil, "rev-parse", "--absolute-git-dir")
	if !ok || gitDir == "" {
		return failed
	}
	// linked worktree ごとに git ディレクトリが分かれるので、一時 index も作業ツリーごとに分かれる。共有すると stat cache が混線する。
	indexEnv := []string{"GIT_INDEX_FILE=" + py.Join(gitDir, snapshotIndexName)}

	// 空の一時 index に add -A すると、作業ツリーそのものの木ができる（read-tree は要らない）。
	if _, ok := runner.git(top, indexEnv, "add", "-A"); !ok {
		return failed
	}
	// add -A は一時 index に無い ignored のファイルを足さないので、本物の index で追跡中の ignored のファイル（add -f で登録したもの）を
	// update-index で取り込む（無くなっていれば --remove で消す）。本物の index を一時 index へコピーしないのは、同じ作業ツリーで並行して
	// 走ったときに、他方の add -A と write-tree の間で一時 index を巻き戻さないため。
	ignored, ok := runner.run(top, nil, "", "ls-files", "-z", "--cached", "--ignored", "--exclude-standard")
	if !ok {
		return failed
	}
	if ignored != "" {
		if _, ok := runner.run(top, indexEnv, ignored, "update-index", "--add", "--remove", "-z", "--stdin"); !ok {
			return failed
		}
	}
	tree, ok := runner.git(top, indexEnv, "write-tree")
	if !ok || tree == "" {
		return failed
	}

	head, _ := runner.git(top, nil, "rev-parse", "--verify", "-q", "HEAD")
	// ref は作業ツリーの間で共有されるので、どの作業ツリーのものかをメッセージに残す。
	// 発火したルールは表示言語に依存しない ID で書く（後から別の言語の環境で reflog を読むことがある）。
	message := "wt-snapshot: " + rules + " @ " + top

	args := []string{"commit-tree"}
	if head != "" {
		// HEAD と同じ木なら保存するものが無い（追跡していない ignored のファイルは元から含まない）。
		if headTree, ok := runner.git(top, nil, "rev-parse", "-q", "HEAD^{tree}"); ok && headTree == tree {
			return clean
		}
		args = append(args, "-p", head)
	}
	args = append(args, "-m", message, tree)

	commit, ok := runner.git(top, snapshotIdentity, args...)
	if !ok || commit == "" {
		return failed
	}
	// 独自の ref の reflog は --create-reflog が無いと記録されない。
	if _, ok := runner.git(top, nil, "update-ref", "--create-reflog", "-m", message, snapshotRef, commit); !ok {
		return failed
	}
	return saved
}

// gitRunner は hook 全体の持ち時間の中で git を起動する。
type gitRunner struct {
	deadline time.Time
}

// git は `git -C <directory> <args...>` を実行し、stdout の両端の空白を除いて返す。失敗・タイムアウトなら ok は偽になる。
func (r gitRunner) git(directory string, env []string, args ...string) (string, bool) {
	output, ok := r.run(directory, env, "", args...)
	return py.Strip(output), ok
}

// run は `git -C <directory> <args...>` を stdin を渡して実行し、stdout をそのまま返す（-z の出力のパスの空白を削らない）。
// 利用者の環境（GIT_* を含む）はそのまま継承し、env の分だけ足す。
func (r gitRunner) run(directory string, env []string, stdin string, args ...string) (string, bool) {
	remaining := time.Until(r.deadline)
	if remaining <= 0 {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), min(gitTimeout, remaining))
	defer cancel()
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, args...)...)
	if env != nil {
		command.Env = append(os.Environ(), env...)
	}
	// タイムアウトでは SIGKILL ではなく SIGTERM で止める。git は SIGTERM を捕捉して一時 index の lock を消すが、
	// SIGKILL だと lock が残り、以後その作業ツリーの snapshot が失敗し続ける（破棄系のコマンドがすべて deny になる）。
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	// SIGTERM で終わらない場合と、子孫のプロセスが出力を開いたまま残った場合に、この猶予の後に kill して待ち続けない。
	command.WaitDelay = time.Second
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	var stdout bytes.Buffer
	command.Stdout = &stdout
	if command.Run() != nil {
		return "", false
	}
	return stdout.String(), true
}
