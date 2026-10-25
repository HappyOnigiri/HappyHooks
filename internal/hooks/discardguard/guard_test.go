package discardguard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HappyOnigiri/hhx/internal/config"
	"github.com/HappyOnigiri/hhx/internal/hookrt"
	"github.com/HappyOnigiri/hhx/internal/hooktest"
	"github.com/HappyOnigiri/hhx/internal/i18n"
	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

// 理由文の目印。「理由:」の 1 行目で、どちらの理由文かを見分ける（ラベルを含まず、JSON でエスケープされる文字も無い）。
var (
	unresolved = reasonMarker(denyUnresolved(hooktest.Language, "x"))
	failedMark = reasonMarker(denyFailed(hooktest.Language, "x"))
)

func reasonMarker(reason string) string {
	return strings.Split(reason, "\n")[2]
}

// snapshotAuthor は snapshot のコミットの作成者の表記である。
const snapshotAuthor = "hhx <hhx@localhost>"

// fixtureIdentity はテスト用のリポジトリを作る git に渡す設定である。CI のランナーには git の利用者設定が無い。
var fixtureIdentity = []string{
	"-c", "user.name=hook-test",
	"-c", "user.email=hook-test@localhost",
	"-c", "commit.gpgsign=false",
	"-c", "init.defaultBranch=main",
}

// TestMain は git を利用者の設定から切り離し、HOME とプロセスの作業ディレクトリを git 管理下でない一時ディレクトリにする。
// payload の cwd が空のときとデバッグ経路では、hook はプロセスの作業ディレクトリを保存しようとする。
// パッケージのディレクトリ（このリポジトリの中）のままだと、開発中のリポジトリに ref を作ってしまう。
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	for key, value := range map[string]string{
		"GIT_CONFIG_GLOBAL":   "/dev/null",
		"GIT_CONFIG_SYSTEM":   "/dev/null",
		"GIT_TERMINAL_PROMPT": "0",
	} {
		_ = os.Setenv(key, value)
	}
	root, err := os.MkdirTemp("", "discard-guard-process-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "nonrepo")
	for _, directory := range []string{home, work} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			panic(err)
		}
	}
	_ = os.Setenv("HOME", home)
	if err := os.Chdir(work); err != nil {
		panic(err)
	}
	return m.Run()
}

// --- git の fixture ---------------------------------------------------------

func gitOutput(dir string, args ...string) (string, error) {
	command := exec.CommandContext(context.Background(), "git", append(append([]string{"-C", dir}, fixtureIdentity...), args...)...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitOutput(dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// repoOptions はテスト用のリポジトリの状態である。零値は「コミットが 1 つあり、未コミットの変更と未追跡ファイルがある」。
type repoOptions struct {
	clean    bool // 未コミットの変更を置かない
	ignored  bool // gitignore の対象の ignored.txt を置く
	noCommit bool // コミットを 1 つも作らない（HEAD の無いリポジトリ）
}

func makeRepo(t *testing.T, path string, options repoOptions) string {
	t.Helper()
	mkdir(t, path)
	gitRun(t, path, "init", "-q")
	if !options.noCommit {
		writeFile(t, filepath.Join(path, "tracked.txt"), "v1\n")
		gitRun(t, path, "add", "tracked.txt")
		gitRun(t, path, "commit", "-q", "-m", "init")
	}
	if options.ignored {
		writeFile(t, filepath.Join(path, ".gitignore"), "ignored.txt\n")
		if !options.noCommit {
			gitRun(t, path, "add", ".gitignore")
			gitRun(t, path, "commit", "-q", "-m", "ignore")
		}
		writeFile(t, filepath.Join(path, "ignored.txt"), "ignored-content\n")
	}
	if !options.clean {
		if !options.noCommit {
			writeFile(t, filepath.Join(path, "tracked.txt"), "v2-uncommitted\n")
		}
		writeFile(t, filepath.Join(path, "untracked.txt"), "new\n")
	}
	return path
}

func refExists(repo, ref string) bool {
	_, err := gitOutput(repo, "rev-parse", "--verify", "-q", ref)
	return err == nil
}

func realpath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// tempDir は symlink を解決した一時ディレクトリを返す（macOS の /var は /private/var への symlink）。
func tempDir(t *testing.T) string {
	t.Helper()
	return realpath(t, t.TempDir())
}

type sandbox struct {
	tmp, repoA, repoB, nested, clean, notARepo string
}

func newSandbox(t *testing.T) sandbox {
	t.Helper()
	tmp := tempDir(t)
	return sandbox{
		tmp:      tmp,
		repoA:    makeRepo(t, filepath.Join(tmp, "repoA"), repoOptions{}),
		repoB:    makeRepo(t, filepath.Join(tmp, "repoB"), repoOptions{}),
		nested:   makeRepo(t, filepath.Join(tmp, "repoA", "sub"), repoOptions{}),
		clean:    makeRepo(t, filepath.Join(tmp, "clean"), repoOptions{clean: true}),
		notARepo: mkdir(t, filepath.Join(tmp, "notarepo")),
	}
}

// --- hook の起動 -------------------------------------------------------------

func runHook(t *testing.T, command, cwd string) hooktest.Result {
	t.Helper()
	return hooktest.Stdin(t, Definition(), hooktest.BashPayload(command, cwd, nil))
}

func expectPass(t *testing.T, command, cwd string) {
	t.Helper()
	if got := runHook(t, command, cwd); got.Decision != "" {
		t.Errorf("command %q in %s: decision=%q, want pass (reason: %q)", command, cwd, got.Decision, got.Reason)
	}
}

func expectDeny(t *testing.T, command, cwd, mark string) {
	t.Helper()
	got := runHook(t, command, cwd)
	if got.Decision != hooktest.Deny || !strings.Contains(got.Reason, mark) {
		t.Errorf("command %q in %s: %+v, want deny with %q", command, cwd, got, mark)
	}
}

// checkProtected は、通しつつ snapshot を作る（ルールが発火している）ことを確かめる。
// 判定だけでは「保存して通した」と「ルールに当たらず素通しした」を区別できないので、ルールの発火も見る。
func checkProtected(t *testing.T, cwd string, commands ...string) {
	t.Helper()
	for _, command := range commands {
		expectPass(t, command, cwd)
		if len(discardRuleIDs(command)) == 0 {
			t.Errorf("command %q matches no rule (nothing is saved)", command)
		}
	}
}

// checkUntouched は、破棄の操作ではないので何もしないことを確かめる。
func checkUntouched(t *testing.T, cwd string, commands ...string) {
	t.Helper()
	for _, command := range commands {
		expectPass(t, command, cwd)
		if labels := discardRuleIDs(command); len(labels) != 0 {
			t.Errorf("command %q is not a discard, but matched %v", command, labels)
		}
	}
}

// --- L1: 通すもの ---------------------------------------------------------

func TestPassThroughResetHard(t *testing.T) {
	box := newSandbox(t)
	checkProtected(t, box.repoA,
		"git reset --hard",
		"git reset --hard HEAD~1",
		"git reset --hard origin/main",
		"git reset --hard origin/main && npm ci",
		"git reset --hard; echo done",
		"(git reset --hard)",
		"git reset -q --hard",
		"rtk git reset --hard",
		"/usr/bin/git reset --hard",
		"git --no-pager reset --hard",
		"git -c user.name=x reset --hard",
		"git -c user.name='a b' reset --hard",
		"git --literal-pathspecs reset --hard",
		"git reset --hard --",
		"git reset -q --hard HEAD",
		// 引用の中だけでも拾う（多めに拾って、対象の特定で絞る方針）。
		"echo 'git reset --hard' > note.txt",
	)
}

func TestPassThroughCheckoutDiscardForms(t *testing.T) {
	box := newSandbox(t)
	checkProtected(t, box.repoA,
		"git checkout -B feature",
		"git checkout -B feature origin/main",
		"git checkout .",
		"git checkout -- .",
		"git checkout -- src/a.ts",
		"git checkout HEAD -- src/a.ts",
		"git checkout HEAD~1 -- .",
		"git checkout -f main",
		"git checkout --force main",
		"git checkout --ours -- conflicted.txt",
		"git checkout :/",
		"git checkout -f",
		"git checkout --",
	)
}

func TestPassThroughSwitchForceForms(t *testing.T) {
	box := newSandbox(t)
	checkProtected(t, box.repoA,
		"git switch -f main",
		"git switch --force main",
		"git switch --discard-changes main",
		"git switch -C feature",
		"git switch -f -",
	)
}

func TestPassThroughRestoreWorktreeForms(t *testing.T) {
	box := newSandbox(t)
	checkProtected(t, box.repoA,
		"git restore .",
		"git restore :/",
		"git restore src/",
		"git restore src/a.ts",
		"git restore --source=main src/a.ts",
		"git restore -s HEAD~1 .",
		"git restore --staged --worktree .",
		"git restore --worktree .",
		"git restore -SW .",
	)
}

func TestPassThroughCleanForms(t *testing.T) {
	box := newSandbox(t)
	checkProtected(t, box.repoA,
		"git clean -f",
		"git clean -fd",
		"git clean -fdx",
		"git clean -xdf",
		"git clean -ffd",
		"git clean -d -f",
		"git clean --force",
		"git clean --force -d",
	)
}

func TestPassThroughApplyDiscardForms(t *testing.T) {
	box := newSandbox(t)
	checkProtected(t, box.repoA,
		"git apply -R fix.patch",
		"git apply --reverse fix.patch",
		"git apply -R --index fix.patch",
		"git apply --3way fix.patch",
		"git apply -3 fix.patch",
		"git apply --reject fix.patch",
		"git apply -R3 fix.patch",
		"git apply -3R fix.patch",
		"git apply -p1 -R fix.patch",
		"git -C "+box.repoB+" apply -R fix.patch",
		// 区切りの先は別のコマンドとして扱う。
		"git apply --check fix.patch && git apply -R fix.patch",
	)
}

func TestPassThroughTargetFromCdOrDashC(t *testing.T) {
	box := newSandbox(t)
	a, b := box.repoA, box.repoB
	checkProtected(t, a,
		"git -C "+b+" reset --hard",
		"git -C"+b+" reset --hard",
		"cd "+b+" && git reset --hard",
		"cd "+b+"\ngit reset --hard", // 改行の区切り
		"cd sub && git clean -fdx",
		"(cd sub && git clean -fdx)",
		"make -C "+b+" build && git reset --hard",          // git 以外の -C は無視する
		"git -C "+b+" fetch && git reset --hard",           // 破棄系以外の -C は無視する
		"git reset --hard && cd "+b+" && echo ok",          // 破棄より後ろの cd は関係ない
		"cd sub && cd .. && git reset --hard",              // cd が複数あっても順に辿る
		"cd "+b+" && git reset --hard && cd "+a,            //
		"git reset --hard && cd "+b+" && git reset --hard", // 破棄系が複数あれば全部保存する
		"git -C "+a+" reset --hard && git -C "+b+" clean -fd",
	)
}

// 破棄しない操作は素通しする（git stash も stash list / apply で戻せる）。
func TestPassThroughNotADiscardOperation(t *testing.T) {
	box := newSandbox(t)
	checkUntouched(t, box.repoA,
		"git status",
		"git log --oneline",
		"git diff",
		"git stash",
		"git stash pop",
		"git stash list",
		"git checkout main",
		"git checkout -b feature",
		"git checkout --track origin/feature",
		"git checkout --detach",
		"git switch main",
		"git switch -c feature",
		"git switch -",
		"git restore --staged foo.txt",
		"git restore",
		"git reset --soft HEAD~1",
		"git reset HEAD~1",
		"git reset",
		"git clean -n",
		"git clean --dry-run",
		"git clean -i",
		"git branch -D old",
		"git rm -r --cached .",
		"git checkout -p",
		"git restore --patch",
		"git worktree list",
		"git fetch --prune",
		// 既定の apply は、文脈が一致しなければ何も書かずに失敗する。
		"git apply fix.patch",
		"git apply --index fix.patch",
		"git apply -p1 fix.patch",
		"git apply --check -R fix.patch",
		"git apply --stat -R fix.patch",
		"git apply --numstat --3way fix.patch",
		"git apply --summary -R fix.patch",
		"git apply --cached -R fix.patch", // index だけに触る
		"git apply -C3 fix.patch",         // -p3 / -C3 の 3 を -3 と誤検出しない
		"git apply -p3 fix.patch",
		"npm run build",
	)
	if refExists(box.repoA, snapshotRef) {
		t.Error("non-discard commands must not create a snapshot")
	}
}

// main worktree の Git 操作とファイルの書き込みは止めない（ブランチ attach の deny は wx の担当で、この hook は持たない）。
func TestMainWorkspacePassThrough(t *testing.T) {
	tmp := tempDir(t)
	clean := makeRepo(t, filepath.Join(tmp, "clean"), repoOptions{clean: true})
	writeFile(t, filepath.Join(clean, ".gitignore"), "ignored/\n")
	gitRun(t, clean, "add", ".gitignore")
	gitRun(t, clean, "commit", "-q", "-m", "ignore generated files")
	mkdir(t, filepath.Join(clean, "ignored"))
	detached := filepath.Join(tmp, "detached")
	gitRun(t, clean, "worktree", "add", "-q", "--detach", detached, "HEAD")

	for _, command := range []string{
		"git checkout --detach HEAD",
		"git switch --detach HEAD",
		"git add tracked.txt",
		"git commit -m test",
		"git reset HEAD -- tracked.txt",
		"git stash push",
		"printf x > tracked.txt",
		"touch new.txt",
		"rm untracked.txt",
		"git status --short",
		"git diff -- tracked.txt",
		"git fetch origin",
		"git add --dry-run tracked.txt",
		"git commit --dry-run",
		"git worktree add --detach " + tmp + "/new-wt HEAD",
	} {
		expectPass(t, command, clean)
	}
	// Codex は exec_command の workdir を PreToolUse へ渡さず、cwd にはセッション開始時の main worktree が入る。
	// detached worktree で実行される commit / checkout --detach を誤って止めない。
	for _, command := range []string{"git commit -m test", "git checkout --detach HEAD"} {
		if got := hooktest.Stdin(t, Definition(), codexExecPayload(command, clean, detached)); got.Decision != "" {
			t.Errorf("codex %q: %+v", command, got)
		}
	}
}

// codexExecPayload は、観測済みの Codex の exec_command から PreToolUse への変換を再現する。
// exec_command の要求は workdir を持つが、PreToolUse の payload には command だけが渡り、cwd はセッション開始時のままになる。
// workdir は、欠落が偶然ではないことをテストから見える形にするために受け取り、意図的に payload へ入れない。
func codexExecPayload(command, sessionCWD, workdir string) string {
	if workdir == "" {
		panic("exec_command の workdir を指定する")
	}
	data, err := json.Marshal(map[string]any{
		"session_id":      "codex-mock-session",
		"transcript_path": "/dev/null",
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"tool_input":      map[string]any{"command": command},
		"cwd":             sessionCWD,
	})
	if err != nil {
		panic(err)
	}
	return string(data)
}

// --- L1: 止めるもの -------------------------------------------------------

func TestDenyTargetNotStaticallyResolvable(t *testing.T) {
	box := newSandbox(t)
	for _, command := range []string{
		"pushd /tmp && git reset --hard",
		"popd && git reset --hard",
		"sh -c 'git reset --hard'",
		"bash -lc 'git reset --hard'",
		"/bin/sh -c 'git clean -fd'",
		"GIT_DIR=/tmp/x git reset --hard",
		"GIT_WORK_TREE=/tmp git clean -fd",
		"GIT_INDEX_FILE=/tmp/i git reset --hard",
		`git -C "$OTHER" reset --hard`,
		"git -C $HOME reset --hard",
		"git -C ~nobody/dev reset --hard", // ~user の形は追わない
		`cd "~/dev" && git reset --hard`,  // クォートつきの ~ はシェルも展開しない
		"git -C 'my repo' reset --hard",
		`git -C "my repo" clean -fd`,
		"git -C * reset --hard",
		"git --git-dir=/tmp/x reset --hard",
		"git --work-tree=/tmp reset --hard",
		"cd && git reset --hard", // 引数の無い cd（ホームへ移る）
		"cd - && git reset --hard",
		`cd sub && cd "$DIR" && git reset --hard`, // 途中の cd が変数
	} {
		expectDeny(t, command, box.repoA, unresolved)
	}
}

// 対象は特定できるが、git 管理下ではない。
func TestDenySnapshotCannotBeCreated(t *testing.T) {
	box := newSandbox(t)
	for _, command := range []string{
		"cd " + box.notARepo + " && git reset --hard",
		"git -C " + box.notARepo + " clean -fd",
		"cd .. && git reset --hard", // サンドボックスの直下は git 管理下でない
	} {
		expectDeny(t, command, box.repoA, failedMark)
	}
}

// 存在しない行き先は追わない（実行時に失敗した cd の後の相対パスを取り違えるため）。
func TestDenyMissingDirectory(t *testing.T) {
	box := newSandbox(t)
	for _, command := range []string{
		"cd " + box.tmp + "/does-not-exist && git reset --hard",
		"cd sub && cd does-not-exist && cd .. && git reset --hard",
	} {
		expectDeny(t, command, box.repoA, unresolved)
	}
	// payload の cwd 自体が無いときも特定できない。
	expectDeny(t, "git reset --hard", filepath.Join(box.tmp, "gone"), unresolved)
}

// payload の cwd が空・欠落・文字列でないときは、プロセスの作業ディレクトリ（TestMain で git 管理下でない場所にしている）を見る。
func TestDenyEmptyCWDFallsBackToProcessCWD(t *testing.T) {
	expectDeny(t, "git reset --hard", "", failedMark)
	for _, raw := range []string{
		`{"tool_input": {"command": "git reset --hard"}}`,
		`{"tool_input": {"command": "git reset --hard"}, "cwd": 123}`,
		`{"tool_input": {"command": "git reset --hard"}, "cwd": null}`,
	} {
		if got := hooktest.Stdin(t, Definition(), raw); got.Decision != hooktest.Deny || !strings.Contains(got.Reason, failedMark) {
			t.Errorf("raw %s: %+v", raw, got)
		}
	}
}

func TestDenyLabelAppearsInReason(t *testing.T) {
	box := newSandbox(t)
	got := runHook(t, "sh -c 'git clean -f && git reset --hard'", box.repoA)
	if got.Decision != hooktest.Deny {
		t.Fatalf("got %+v", got)
	}
	// discardRules の並び順で連結する。
	if want := denyUnresolved(hooktest.Language, ruleLabels(hooktest.Language, []string{ruleResetHard, ruleClean})); got.Reason != want {
		t.Errorf("reason %q", got.Reason)
	}
}

// 回避を思いとどまらせるのは理由文だけなので、迂回路を案内せず、ユーザーに伝えるよう促す。
func TestReasonsDoNotSuggestWorkarounds(t *testing.T) {
	for _, reason := range []string{
		denyUnresolved(i18n.English, "x"), denyFailed(i18n.English, "x"),
		denyUnresolved(i18n.Japanese, "x"), denyFailed(i18n.Japanese, "x"),
	} {
		// Claude Code と Codex の両方に出るので、片方にしか無いツール名を書かない。
		for _, tool := range []string{"Write", "Edit", "apply_patch"} {
			if strings.Contains(reason, tool) {
				t.Errorf("reason must not name the %s tool: %q", tool, reason)
			}
		}
	}
	for _, language := range []i18n.Language{i18n.English, i18n.Japanese} {
		if !strings.Contains(denyUnresolved(language, "x"), "cp ") {
			t.Errorf("%s: unresolved reason must rule out cp backups", language)
		}
	}
}

// --- L1: ゲートと奇妙な入力 -----------------------------------------------

func TestGateNonGitCommands(t *testing.T) {
	for _, command := range []string{"ls -la", "npm ci", "echo legit", "rm -rf build", "make clean", "docker system prune -f"} {
		expectPass(t, command, "")
	}
	if gate([]byte(`{"tool_input": {"command": "ls"}}`)) || !gate([]byte("git")) {
		t.Error("gate must look for git in the raw input")
	}
	// git を含んでいても、git の起動に見えなければ解析しない（プロセスの作業ディレクトリは git 管理下でないので、解析すれば deny になる）。
	for _, command := range []string{"echo digit reset --hard", "gitk", "legit reset --hard", "git"} {
		expectPass(t, command, "")
	}
}

func TestOddInputsPassSilently(t *testing.T) {
	for _, raw := range []string{
		"", "null", "[]", "{}", "git", `"git reset --hard"`, "not json git reset --hard",
		`{"tool_input": "git reset --hard"}`,
		`{"tool_input": {"command": ["git reset --hard"]}}`,
		`{"tool_input": {"command": 1}, "git": 1}`,
		`{"tool_input": {"command": null}, "git": 1}`,
		`{"tool_input": {"command": ""}, "git": 1}`,
		`{"tool_input": null, "git": 1}`,
		`{"git": {"command": "git reset --hard"}}`,
	} {
		if got := hooktest.Stdin(t, Definition(), raw); got.Decision != "" {
			t.Errorf("raw %q: %+v", raw, got)
		}
	}
}

func TestTrailingNewline(t *testing.T) {
	box := newSandbox(t)
	checkProtected(t, box.repoA, "git reset --hard\n", "git clean --force\n", "git checkout .\n", "git switch -f\n")
	expectDeny(t, "git reset --hard\n", box.notARepo, failedMark)
}

func TestDisabledByConfig(t *testing.T) {
	box := newSandbox(t)
	got := hooktest.Run(t, Definition(), hooktest.BashPayload("git reset --hard", box.notARepo, nil),
		hooktest.Options{Config: "hooks:\n  discard-guard:\n    enabled: false\n"})
	if got.Decision != "" {
		t.Errorf("disabled hook must pass: %+v", got)
	}
	got = hooktest.Run(t, Definition(), "", hooktest.Options{
		Config: "hooks:\n  discard-guard:\n    enabled: false\n", Args: []string{"git reset --hard"},
	})
	if got.Decision != "" {
		t.Errorf("disabled hook must pass on the argv path: %+v", got)
	}
}

// Python の \s は Unicode の空白に一致し、$ は末尾の改行の直前にも一致する。期待値は移植元の Python 実装で求めたものである。
func TestUnicodeSpacesAndNewlinesMatchPython(t *testing.T) {
	cases := map[string][]string{
		"git reset\u3000--hard":         {ruleResetHard},
		"git\u3000reset --hard":         {ruleResetHard},
		"echo x;\u00a0git clean -f":     {ruleClean},
		"git reset --hard\u3000":        {ruleResetHard},
		"git clean\x1c-f":               {ruleClean},
		"git checkout\u0085.":           {ruleCheckout},
		"git reset --hard\n":            {ruleResetHard},
		"git clean --force\n":           {ruleClean},
		"git switch -f\n":               {ruleSwitch},
		"git checkout .\n":              {ruleCheckout},
		"git restore --staged\u3000foo": nil,
		"git restore\u3000--staged foo": nil,
		"git restore\u3000foo":          {ruleRestore},
		"git apply\u3000-R fix.patch":   {ruleApply},
		"git apply -R\u2028fix.patch":   {ruleApply},
		"git apply --check\u00a0-R x":   nil,
		"git -C\u3000/tmp reset --hard": {ruleResetHard},
		"rtk\u3000git reset --hard":     {ruleResetHard},
		"git reset --hard\r":            {ruleResetHard},
		"git reset --hard\u200b":        nil, // U+200B は Python でも空白ではない
		"git reset --hard\x1f":          {ruleResetHard},
		"git reset --hard\x0b":          {ruleResetHard},
	}
	for command, want := range cases {
		if got := discardRuleIDs(command); !reflect.DeepEqual(got, want) {
			t.Errorf("labels(%q) = %v, want %v", command, got, want)
		}
		if !gitCallRE.MatchString(command) {
			t.Errorf("%q must pass the second gate", command)
		}
	}
	if gitCallRE.MatchString("git\u200breset --hard") {
		t.Error("U+200B is not a space in Python")
	}
	// トークンの分割も Python の str.split() と同じく Unicode の空白で行う。
	box := newSandbox(t)
	got, err := resolveTargetDirs("cd\u3000sub && git\u00a0reset --hard", box.repoA)
	if err != nil || !reflect.DeepEqual(got, []string{filepath.Join(box.repoA, "sub")}) {
		t.Errorf("resolve = %v, %v", got, err)
	}
}

// --- L2: どのルールが発火するか ---------------------------------------------

func TestDiscardRulesSingle(t *testing.T) {
	for command, want := range map[string][]string{
		"git reset --hard":   {ruleResetHard},
		"git checkout .":     {ruleCheckout},
		"git switch -f main": {ruleSwitch},
		"git restore src/":   {ruleRestore},
		"git clean -fd":      {ruleClean},
		"git apply -R x":     {ruleApply},
	} {
		if got := discardRuleIDs(command); !reflect.DeepEqual(got, want) {
			t.Errorf("labels(%q) = %v, want %v", command, got, want)
		}
	}
	if got := discardRuleIDs("git clean -f && git reset --hard"); !reflect.DeepEqual(got, []string{ruleResetHard, ruleClean}) {
		t.Errorf("multiple rules: %v", got)
	}
}

func TestDiscardRulesNone(t *testing.T) {
	for _, command := range []string{
		"git status", "git stash", "git checkout main", "git switch main", "git restore --staged foo", "git clean -n",
		"git reset --soft HEAD~1", "git checkout -b topic", "git switch -c topic", "ls -la",
	} {
		if got := discardRuleIDs(command); len(got) != 0 {
			t.Errorf("labels(%q) = %v, want none", command, got)
		}
	}
}

// discardRules で拾うサブコマンドは discardSubcommands に入っていること。
// 漏らすと git -C <別のリポジトリ> を採用せず、無関係なリポジトリを保存してしまう。
func TestEveryRuleSubcommandIsResolvable(t *testing.T) {
	samples := map[string]string{
		"reset":    "git reset --hard",
		"checkout": "git checkout .",
		"switch":   "git switch -f main",
		"restore":  "git restore .",
		"clean":    "git clean -fd",
		"apply":    "git apply -R fix.patch",
	}
	if len(samples) != len(discardSubcommands) || len(discardRules) != len(discardSubcommands) {
		t.Fatalf("every rule needs a subcommand and a sample: %d rules, %d subcommands", len(discardRules), len(discardSubcommands))
	}
	for _, subcommand := range discardSubcommands {
		command, ok := samples[subcommand]
		if !ok {
			t.Errorf("no sample for %s", subcommand)
			continue
		}
		if len(discardRuleIDs(command)) == 0 {
			t.Errorf("%q matches no rule", command)
		}
		other := t.TempDir()
		got, err := resolveTargetDirs(strings.Replace(command, "git ", "git -C "+other+" ", 1), "/")
		if err != nil || !reflect.DeepEqual(got, []string{other}) {
			t.Errorf("-C on %s must be adopted: %v, %v", subcommand, got, err)
		}
	}
}

func TestRestoreStagedOnlyIsNotADiscard(t *testing.T) {
	for _, command := range []string{"git restore --staged foo.txt", "git restore --staged .", "git restore --staged=x ."} {
		if isWorktreeRestore(command) {
			t.Errorf("%q is staged only", command)
		}
	}
	for _, command := range []string{"git restore --staged --worktree .", "git restore --worktree foo", "git restore ."} {
		if !isWorktreeRestore(command) {
			t.Errorf("%q restores the worktree", command)
		}
	}
	// パススペックが要る。
	for _, command := range []string{"git restore", "git restore --patch"} {
		if isWorktreeRestore(command) {
			t.Errorf("%q has no pathspec", command)
		}
	}
	// 1 つのコマンドに複数あるとき、破棄する側があれば拾う。
	if !isWorktreeRestore("git restore --staged foo && git restore bar") {
		t.Error("the second restore discards")
	}
}

// --- L2: snapshot の対象のディレクトリ ---------------------------------------

type resolveFixture struct {
	tmp, a, b string
}

func newResolveFixture(t *testing.T) resolveFixture {
	t.Helper()
	tmp := tempDir(t)
	fixture := resolveFixture{tmp: tmp, a: filepath.Join(tmp, "a"), b: filepath.Join(tmp, "b")}
	for _, path := range []string{fixture.a, fixture.b, filepath.Join(fixture.a, "sub")} {
		mkdir(t, path)
	}
	return fixture
}

func assertTargets(t *testing.T, command, cwd string, want ...string) {
	t.Helper()
	got, err := resolveTargetDirs(command, cwd)
	if err != nil || got == nil {
		t.Errorf("%q: unresolved (%v)", command, err)
		return
	}
	normalized := make([]string, len(got))
	for index, path := range got {
		normalized[index] = py.Normpath(path)
	}
	expected := make([]string, len(want))
	for index, path := range want {
		expected[index] = py.Normpath(path)
	}
	if !reflect.DeepEqual(normalized, expected) {
		t.Errorf("%q: targets %v, want %v", command, normalized, expected)
	}
}

func TestResolveUsesCWDByDefault(t *testing.T) {
	f := newResolveFixture(t)
	for _, command := range []string{
		"git reset --hard", "git clean -fd", "git checkout .", "echo git reset --hard",
		// クォートの中は git のトークンとして拾えないが、多めに拾う方針で cwd を保存する。
		"echo 'git reset --hard' > note.txt",
	} {
		assertTargets(t, command, f.a, f.a)
	}
}

func TestResolveDashCOnDiscardCall(t *testing.T) {
	f := newResolveFixture(t)
	assertTargets(t, "git -C "+f.b+" reset --hard", f.a, f.b)
	assertTargets(t, "git -C"+f.b+" clean -fd", f.a, f.b)
	assertTargets(t, "git -C "+f.b+" switch -f main", f.a, f.b)
	assertTargets(t, "git -C "+f.b+" restore .", f.a, f.b)
}

// クォートで空白を含む値。値の途中で切れてサブコマンドを取り違えないこと。
func TestResolveQuotedOptionValues(t *testing.T) {
	f := newResolveFixture(t)
	assertTargets(t, "git -c user.name='a b' reset --hard", f.a, f.a)
	assertTargets(t, `git -c user.name="a b" -c x=y clean -fd`, f.a, f.a)
	// -C の値がクォートつきなら静的に解決できない。
	for _, command := range []string{"git -C 'a b' reset --hard", `git -C "a b" reset --hard`, "git -C'a b' reset --hard"} {
		if got, _ := resolveTargetDirs(command, f.a); got != nil {
			t.Errorf("%q must be unresolved: %v", command, got)
		}
	}
}

func TestResolveDashCElsewhereIsIgnored(t *testing.T) {
	f := newResolveFixture(t)
	assertTargets(t, "make -C "+f.b+" build && git reset --hard", f.a, f.a)
	assertTargets(t, "git -C "+f.b+" fetch && git reset --hard", f.a, f.a)
	assertTargets(t, "git -C "+f.b+" status; git clean -fd", f.a, f.a)
}

func TestResolveCdBeforeDiscard(t *testing.T) {
	f := newResolveFixture(t)
	sub := filepath.Join(f.a, "sub")
	assertTargets(t, "cd "+f.b+" && git reset --hard", f.a, f.b)
	assertTargets(t, "cd sub && git reset --hard", f.a, sub)
	assertTargets(t, "cd ./sub && git clean -fd", f.a, sub)
	assertTargets(t, "cd "+f.b+"\ngit reset --hard", f.a, f.b)
	assertTargets(t, "(cd "+f.b+" && git reset --hard)", f.a, f.b)
	assertTargets(t, "true;cd "+f.b+";git reset --hard", f.a, f.b)
}

func TestResolveCdAfterDiscardIsIgnored(t *testing.T) {
	f := newResolveFixture(t)
	assertTargets(t, "git reset --hard && cd "+f.b, f.a, f.a)
	assertTargets(t, "git reset --hard; cd "+f.b+"; echo done", f.a, f.a)
}

func TestResolveDashCAppliesAfterCd(t *testing.T) {
	f := newResolveFixture(t)
	sub := filepath.Join(f.a, "sub")
	assertTargets(t, "cd sub && git -C . reset --hard", f.a, sub)
	assertTargets(t, "cd sub && git -C "+f.b+" reset --hard", f.a, f.b)
	assertTargets(t, "cd sub; git -C .. reset --hard", f.a, f.a)
	assertTargets(t, "git -C sub reset --hard", f.a, sub)
	assertTargets(t, "git -C ./sub clean -fd", f.a, sub)
}

// cd が複数あっても順に辿り、破棄した時点の作業ディレクトリを対象にする。
func TestResolveCdChain(t *testing.T) {
	f := newResolveFixture(t)
	sub := filepath.Join(f.a, "sub")
	assertTargets(t, "cd sub && cd .. && git reset --hard", f.a, f.a)
	assertTargets(t, "cd "+f.b+" && cd "+f.a+" && git reset --hard", f.a, f.a)
	assertTargets(t, "cd sub; cd ..; cd sub; git clean -fd", f.a, sub)
	assertTargets(t, "cd sub && cd ../../b && git reset --hard", f.a, f.b)
	assertTargets(t, "cd "+f.b+"\ncd "+f.a+"/sub\ngit checkout -- x", f.a, sub)
}

// 先頭の ~ / ~/ だけは展開する（シェルと同じ HOME なので決まる）。
func TestResolveTildePrefixIsExpanded(t *testing.T) {
	f := newResolveFixture(t)
	home := mkdir(t, filepath.Join(f.tmp, "home"))
	t.Setenv("HOME", home)
	assertTargets(t, "cd ~ && git reset --hard", f.a, home)
	assertTargets(t, "cd ~/ && git reset --hard", f.a, home)
	assertTargets(t, "git -C ~ reset --hard", f.a, home)
	assertTargets(t, "git -C~ clean -fd", f.a, home)
	// 展開した後の相対の cd も追える。
	assertTargets(t, "cd ~ && cd "+f.a+" && git reset --hard", f.a, f.a)
	// HOME の末尾のスラッシュは除く。HOME が / なら ~/x は /x になる（Python の os.path.expanduser と同じ）。
	t.Setenv("HOME", home+"/")
	assertTargets(t, "cd ~/ && git reset --hard", f.a, home+"/")
	t.Setenv("HOME", "/")
	assertTargets(t, "cd ~ && git reset --hard", f.a, "/")
	if got := expandUser("~" + f.a); got != f.a {
		t.Errorf("expandUser with HOME=/ = %q, want %q", got, f.a)
	}
}

// 病的に長い入力でも、正規表現とトークンの走査が破綻しない。
func TestPathologicalInputIsFast(t *testing.T) {
	f := newResolveFixture(t)
	cases := []string{
		"git " + strings.Repeat("-c a=b ", 200) + "reset --hard",
		"git -C " + strings.Repeat("'", 300) + " reset --hard",
		"git reset " + strings.Repeat("x", 20000) + " --hard",
		"git reset --hard" + strings.Repeat(" && cd x", 500),
		"git -C " + strings.Repeat("'a ", 2000) + "reset --hard",
		strings.Repeat("git restore --staged x; ", 2000),
	}
	start := time.Now()
	for _, command := range cases {
		_, _ = resolveTargetDirs(command, f.a)
		discardRuleIDs(command)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %v", elapsed)
	}
}

func TestResolveUnresolvable(t *testing.T) {
	f := newResolveFixture(t)
	t.Setenv("HOME", f.tmp)
	for _, command := range []string{
		"pushd /tmp && git reset --hard",
		"popd; git reset --hard",
		"sh -c 'git reset --hard'",
		"bash -lc 'git reset --hard'",
		"/bin/zsh -c 'git reset --hard'",
		"GIT_DIR=/tmp git reset --hard",
		"GIT_WORK_TREE=/tmp git reset --hard",
		"GIT_INDEX_FILE=/tmp/i git reset --hard",
		"git --git-dir=/tmp/x reset --hard",
		"git --work-tree=/tmp reset --hard",
		"git -C",
		"cd && git reset --hard",
		"cd -- && git reset --hard",
		"cd sub && cd $X && git reset --hard",            // 途中の cd が変数
		"cd sub && cd nope && cd .. && git reset --hard", // 途中の cd の行き先が無い
		`git -C "$X" reset --hard`,
		"git -C $X reset --hard",
		"git -C `pwd` reset --hard",
		"git -C 'a b' reset --hard",
		"git -C ~nobody/x reset --hard",
		`cd "~/x" && git reset --hard`,
		"cd ~/does-not-exist-discard-guard-test && git reset --hard",
		"git -C */repo reset --hard",
		"cd /does/not/exist && git reset --hard",
		"git -C /does/not/exist reset --hard",
	} {
		if got, err := resolveTargetDirs(command, f.a); got != nil || err != nil {
			t.Errorf("%q must be unresolved: %v, %v", command, got, err)
		}
	}
	if got, err := resolveTargetDirs("git reset --hard", filepath.Join(f.tmp, "missing")); got != nil || err != nil {
		t.Errorf("missing cwd must be unresolved: %v, %v", got, err)
	}
}

// 破棄系が複数あれば、それぞれの時点のディレクトリを出現順に全部候補にする。
func TestResolveMultipleDiscardsCollectEveryTarget(t *testing.T) {
	f := newResolveFixture(t)
	sub := filepath.Join(f.a, "sub")
	assertTargets(t, "git -C "+f.a+" reset --hard && git -C "+f.b+" clean -fd", f.a, f.a, f.b)
	assertTargets(t, "cd "+f.b+" && git reset --hard && cd "+f.a, f.a, f.b)
	assertTargets(t, "git reset --hard && cd "+f.b+" && git clean -fd", f.a, f.a, f.b)
	assertTargets(t, "cd sub && git clean -fd && cd "+f.b+" && git reset --hard", f.a, sub, f.b)
}

func TestResolveSameTargetTwiceIsListedOnce(t *testing.T) {
	f := newResolveFixture(t)
	assertTargets(t, "git -C "+f.b+" reset --hard && git -C "+f.b+" clean -fd", f.a, f.b)
	assertTargets(t, "git reset --hard && cd "+f.a+" && git clean -fd", f.a, f.a)
}

// 1 つの git に -C が複数あれば、git と同じく出現順に適用し、相対パスは直前の -C の先から辿る。
// 移植元は最後の -C だけを cwd から解決していたので、cwd にも同じ名前のディレクトリがあると別のリポジトリを保存していた（意図して変えた点）。
func TestResolveMultipleDashCAreApplied(t *testing.T) {
	f := newResolveFixture(t)
	sub := filepath.Join(f.a, "sub")
	// cwd（b）にも sub があるが、対象は a/sub である。
	mkdir(t, filepath.Join(f.b, "sub"))
	assertTargets(t, "git -C "+f.a+" -C sub reset --hard", f.b, sub)
	assertTargets(t, "git -C"+f.a+" -Csub clean -fd", f.b, sub)
	assertTargets(t, "git -C sub -C .. reset --hard", f.a, f.a)
	// 絶対パスの -C は、それより前の -C を打ち消す。
	assertTargets(t, "git -C sub -C "+f.b+" reset --hard", f.a, f.b)
	assertTargets(t, "cd "+f.a+" && git -C sub -C . reset --hard", f.b, sub)
	// 途中の -C が解決できなければ、特定できないものとする。
	for _, command := range []string{"git -C nope -C sub reset --hard", "git -C sub -C $X reset --hard"} {
		if got, err := resolveTargetDirs(command, f.a); got != nil || err != nil {
			t.Errorf("%q must be unresolved: %v, %v", command, got, err)
		}
	}
}

// ( ... ) と $( ... ) の中の cd は、閉じ括弧の後に残らない。
// 移植元は括弧を空白として読んでいたので、閉じた後の破棄も cd の先を保存し、外側の変更を保存し損ねていた（意図して変えた点）。
// 括弧はクォートや case の分岐を見分けずに数えるので、括弧を無視した作業ディレクトリ（移植元の意味）も候補に残す。
func TestResolveCdInsideSubshellDoesNotLeak(t *testing.T) {
	f := newResolveFixture(t)
	sub := filepath.Join(f.a, "sub")
	assertTargets(t, "(cd "+f.b+" && git reset --hard); git reset --hard", f.a, f.b, f.a)
	assertTargets(t, "(cd "+f.b+"); git reset --hard", f.a, f.a, f.b)
	assertTargets(t, "(cd "+f.b+")&&git clean -fd", f.a, f.a, f.b)
	assertTargets(t, "x=$(cd "+f.b+" && pwd); git reset --hard", f.a, f.a, f.b)
	// 入れ子のサブシェルは 1 段ずつ戻る。括弧を無視した側は最後の cd の先に留まる。
	assertTargets(t, "(cd sub && (cd "+f.b+") && git reset --hard) && git clean -fd", f.a, sub, f.b, f.a)
	// { ... } はサブシェルではないので、cd は残る。
	assertTargets(t, "{ cd "+f.b+"; }; git reset --hard", f.a, f.b)
	// 対応の無い閉じ括弧（case の分岐など）は無視する。
	assertTargets(t, "case x in x) cd "+f.b+";; esac; git reset --hard", f.a, f.b)
	// 破棄系の git をトークンとして拾えないときに保存する「最後の作業ディレクトリ」も、両方を候補にする。
	assertTargets(t, "(cd "+f.b+"); echo 'git reset --hard'", f.a, f.a, f.b)
}

// サブシェルを閉じない括弧（クォートの中、case の分岐）で cd が取り消されても、実際に破棄する cd の先を保存する。
func TestResolveParenthesesThatDoNotCloseASubshellKeepTheCdTarget(t *testing.T) {
	f := newResolveFixture(t)
	assertTargets(t, "(cd "+f.b+" && echo ':)' && git reset --hard)", f.a, f.a, f.b)
	assertTargets(t, "(cd "+f.b+" && case x in x) echo;; esac && git reset --hard)", f.a, f.a, f.b)
	assertTargets(t, "echo '(' && cd "+f.b+" && echo ')' && git reset --hard", f.a, f.a, f.b)
	// 括弧を無視した側で cd の先が解決できなければ、特定できないものとして deny する。
	if got, err := resolveTargetDirs("(cd sub); cd sub && git reset --hard", f.a); got != nil || err != nil {
		t.Errorf("must be unresolved: %v, %v", got, err)
	}
}

func TestTokenizeKeepsTokensAndRecordsSeparators(t *testing.T) {
	tokens := tokenize("(cd a&&git -C b reset)|x $(y) ((z))\nw")
	wantTokens := []string{"cd", "a", "git", "-C", "b", "reset", "x", "$", "y", "z", "w"}
	if got := tokenTexts(tokens); !reflect.DeepEqual(got, wantTokens) {
		t.Fatalf("tokens = %q", got)
	}
	want := map[int]string{0: "(", 2: "&&", 6: ")|", 8: "(", 9: ")((", 10: "))\n"}
	for index, token := range tokens {
		if token.seps != want[index] {
			t.Errorf("seps before %q = %q, want %q", token.text, token.seps, want[index])
		}
	}
	// 移植元のトークン（区切りを空白に置き換えて分けたもの）と同じ列になる。
	command := "a(b)c{d}e;f&g|h　(i"
	if got, want := tokenTexts(tokenize(command)), py.Fields(regexp.MustCompile(`[(){};&|]`).ReplaceAllString(command, " ")); !reflect.DeepEqual(got, want) {
		t.Errorf("tokens = %q, want %q", got, want)
	}
	// ${NAME} だけは 1 語にする。名前以外を囲む形は移植元と同じく区切る。
	if got, want := tokenTexts(tokenize("cd ${S}/x; echo ${X:-$(cd /y)}")), []string{"cd", "${S}/x", "echo", "$", "X:-$", "cd", "/y"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tokens = %q, want %q", got, want)
	}
	if got := tokenize("ab  cd")[1].offset; got != 4 {
		t.Errorf("offset = %d", got)
	}
}

func tokenTexts(tokens []token) []string {
	texts := make([]string, len(tokens))
	for index, token := range tokens {
		texts[index] = token.text
	}
	return texts
}

func TestHelpers(t *testing.T) {
	if !isStaticPath("/tmp/a-b_c.d") {
		t.Error("plain path must be static")
	}
	for _, path := range []string{"$HOME", "`pwd`", "a*", "a?", "~/x", "'a'", `"a"`, "a&b", "a;b", "a|b", "a(b", "a)b"} {
		if isStaticPath(path) {
			t.Errorf("%q must not be static", path)
		}
	}
	for _, token := range []string{"sh", "bash", "zsh", "dash", "ksh", "/bin/sh", "/usr/local/bin/bash"} {
		if !isShell(token) {
			t.Errorf("%q is a shell", token)
		}
	}
	for _, token := range []string{"ssh", "shell", "bashrc", "git", "/bin/ls"} {
		if isShell(token) {
			t.Errorf("%q is not a shell", token)
		}
	}
}

// --- L3: 本物の git で snapshot を作り、復元できること ------------------------

func tempRepo(t *testing.T, options repoOptions) string {
	t.Helper()
	return makeRepo(t, filepath.Join(tempDir(t), "repo"), options)
}

func show(t *testing.T, repo, object string) string {
	t.Helper()
	return gitRun(t, repo, "show", object)
}

func TestSnapshotSavesTrackedAndUntracked(t *testing.T) {
	repo := tempRepo(t, repoOptions{})
	expectPass(t, "git reset --hard", repo)
	if !refExists(repo, snapshotRef) {
		t.Fatal("snapshot ref is missing")
	}
	if got := show(t, repo, snapshotRef+":tracked.txt"); got != "v2-uncommitted" {
		t.Errorf("tracked.txt = %q", got)
	}
	if got := show(t, repo, snapshotRef+":untracked.txt"); got != "new" {
		t.Errorf("untracked.txt = %q", got)
	}
}

// 追跡中で gitignore にも一致するファイル（add -f で登録したもの）の変更と削除も snapshot に入る。
// 入らないと、reset --hard で変更が消えるうえに、README の手順での復元がそのファイルを削除する。
func TestSnapshotSavesTrackedIgnoredFiles(t *testing.T) {
	repo := tempRepo(t, repoOptions{clean: true})
	writeFile(t, filepath.Join(repo, ".gitignore"), "*.env\n")
	writeFile(t, filepath.Join(repo, " spaced.env"), "v1\n")
	writeFile(t, filepath.Join(repo, "removed.env"), "v1\n")
	gitRun(t, repo, "add", ".gitignore")
	gitRun(t, repo, "add", "-f", " spaced.env", "removed.env")
	gitRun(t, repo, "commit", "-q", "-m", "tracked ignored")
	writeFile(t, filepath.Join(repo, " spaced.env"), "v2-uncommitted\n")
	if err := os.Remove(filepath.Join(repo, "removed.env")); err != nil {
		t.Fatal(err)
	}
	status := gitRun(t, repo, "status", "--porcelain")

	expectPass(t, "git reset --hard", repo)
	if got := show(t, repo, snapshotRef+": spaced.env"); got != "v2-uncommitted" {
		t.Errorf(" spaced.env = %q", got)
	}
	if got := gitRun(t, repo, "ls-tree", "--name-only", snapshotRef, "removed.env"); got != "" {
		t.Errorf("removed.env must be absent from the snapshot: %q", got)
	}
	// 本物の index と作業ツリーには触らない。
	if got := gitRun(t, repo, "status", "--porcelain"); got != status {
		t.Errorf("status changed: %q -> %q", status, got)
	}
}

func TestSnapshotSavesStagedChanges(t *testing.T) {
	repo := tempRepo(t, repoOptions{})
	writeFile(t, filepath.Join(repo, "staged.txt"), "staged\n")
	gitRun(t, repo, "add", "staged.txt")
	expectPass(t, "git reset --hard", repo)
	if got := show(t, repo, snapshotRef+":staged.txt"); got != "staged" {
		t.Errorf("staged.txt = %q", got)
	}
}

func TestSnapshotLeavesWorktreeAndIndexUntouched(t *testing.T) {
	repo := tempRepo(t, repoOptions{})
	writeFile(t, filepath.Join(repo, "staged.txt"), "staged\n")
	gitRun(t, repo, "add", "staged.txt")
	status := gitRun(t, repo, "status", "--porcelain")
	index := gitRun(t, repo, "ls-files", "-s")
	expectPass(t, "git reset --hard", repo)
	if got := gitRun(t, repo, "status", "--porcelain"); got != status {
		t.Errorf("status changed: %q -> %q", status, got)
	}
	if got := gitRun(t, repo, "ls-files", "-s"); got != index {
		t.Errorf("index changed: %q -> %q", index, got)
	}
}

// 破棄した後、snapshot から中身を戻せる（README の復元の手順）。
func TestSnapshotIsRestorable(t *testing.T) {
	repo := tempRepo(t, repoOptions{})
	expectPass(t, "git reset --hard && git clean -fd", repo)
	gitRun(t, repo, "reset", "--hard", "-q")
	gitRun(t, repo, "clean", "-fdq")
	if got := readFile(t, filepath.Join(repo, "tracked.txt")); got != "v1\n" {
		t.Fatalf("discard did not happen: %q", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "untracked.txt")); !os.IsNotExist(err) {
		t.Fatal("untracked.txt must be gone")
	}
	gitRun(t, repo, "checkout", snapshotRef, "--", ".")
	if got := readFile(t, filepath.Join(repo, "tracked.txt")); got != "v2-uncommitted\n" {
		t.Errorf("tracked.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(repo, "untracked.txt")); got != "new\n" {
		t.Errorf("untracked.txt = %q", got)
	}
}

func TestSnapshotParentIsHeadAndAuthorIsHook(t *testing.T) {
	repo := tempRepo(t, repoOptions{})
	head := gitRun(t, repo, "rev-parse", "HEAD")
	expectPass(t, "git reset --hard", repo)
	if got := gitRun(t, repo, "rev-parse", snapshotRef+"^"); got != head {
		t.Errorf("parent = %q, want %q", got, head)
	}
	if got := gitRun(t, repo, "log", "-1", "--format=%an <%ae>", snapshotRef); got != snapshotAuthor {
		t.Errorf("author = %q", got)
	}
	if got := gitRun(t, repo, "log", "-1", "--format=%cn <%ce>", snapshotRef); got != snapshotAuthor {
		t.Errorf("committer = %q", got)
	}
	if got := gitRun(t, repo, "log", "-1", "--format=%s", snapshotRef); got != "wt-snapshot: "+ruleResetHard+" @ "+repo {
		t.Errorf("subject = %q", got)
	}
}

func TestSnapshotReflogAccumulates(t *testing.T) {
	repo := tempRepo(t, repoOptions{})
	expectPass(t, "git reset --hard", repo)
	first := gitRun(t, repo, "rev-parse", snapshotRef)
	writeFile(t, filepath.Join(repo, "tracked.txt"), "v3-uncommitted\n")
	expectPass(t, "git clean -fd", repo)
	if second := gitRun(t, repo, "rev-parse", snapshotRef); second == first {
		t.Error("second snapshot must move the ref")
	}
	reflog := strings.Split(gitRun(t, repo, "reflog", "show", snapshotRef), "\n")
	if len(reflog) != 2 || !strings.Contains(reflog[0], ruleClean) {
		t.Errorf("reflog = %q", reflog)
	}
	// 古い方も reflog から辿れる。
	if got := gitRun(t, repo, "rev-parse", snapshotRef+"@{1}"); got != first {
		t.Errorf("@{1} = %q, want %q", got, first)
	}
}

func TestSnapshotCleanWorktreeCreatesNoRef(t *testing.T) {
	repo := tempRepo(t, repoOptions{clean: true})
	expectPass(t, "git reset --hard", repo)
	if refExists(repo, snapshotRef) {
		t.Error("clean worktree must not create a snapshot")
	}
}

func TestSnapshotNonDiscardCommandCreatesNoRef(t *testing.T) {
	repo := tempRepo(t, repoOptions{})
	expectPass(t, "git status", repo)
	if refExists(repo, snapshotRef) {
		t.Error("non-discard command must not create a snapshot")
	}
}

// HEAD の無いリポジトリでも、親の無いコミットとして保存する。
func TestSnapshotRepoWithoutCommits(t *testing.T) {
	repo := tempRepo(t, repoOptions{noCommit: true})
	expectPass(t, "git clean -fd", repo)
	if got := show(t, repo, snapshotRef+":untracked.txt"); got != "new" {
		t.Errorf("untracked.txt = %q", got)
	}
	if got := gitRun(t, repo, "rev-list", "--count", snapshotRef); got != "1" {
		t.Errorf("commits = %q", got)
	}
}

// 既知の限界: gitignore の対象は保存しない（clean -fdx では救えない）。
func TestSnapshotGitignoredFileIsNotSaved(t *testing.T) {
	repo := tempRepo(t, repoOptions{ignored: true})
	expectPass(t, "git clean -fdx", repo)
	if _, err := gitOutput(repo, "cat-file", "-e", snapshotRef+":ignored.txt"); err == nil {
		t.Error("ignored.txt must not be saved")
	}
	if got := gitRun(t, repo, "ls-tree", "--name-only", snapshotRef); got != ".gitignore\ntracked.txt\nuntracked.txt" {
		t.Errorf("tree = %q", got)
	}
}

func TestSnapshotUnusualFilenames(t *testing.T) {
	repo := tempRepo(t, repoOptions{})
	writeFile(t, filepath.Join(repo, "日本語ファイル.txt"), "にほんご\n")
	writeFile(t, filepath.Join(repo, "a file with spaces.txt"), "x\n")
	expectPass(t, "git reset --hard", repo)
	if got := show(t, repo, snapshotRef+":日本語ファイル.txt"); got != "にほんご" {
		t.Errorf("unicode file = %q", got)
	}
	if got := show(t, repo, snapshotRef+":a file with spaces.txt"); got != "x" {
		t.Errorf("spaced file = %q", got)
	}
}

// cd で入れ子のリポジトリに入った場合は、内側だけを保存する。
func TestSnapshotNestedRepoTargetsInner(t *testing.T) {
	outer := tempRepo(t, repoOptions{})
	inner := makeRepo(t, filepath.Join(outer, "inner"), repoOptions{})
	expectPass(t, "cd inner && git reset --hard", outer)
	if !refExists(inner, snapshotRef) || refExists(outer, snapshotRef) {
		t.Error("only the inner repository must be saved")
	}
}

// cd の先のリポジトリだけを保存し、cwd の側には作らない。
func TestSnapshotCdTargetOnly(t *testing.T) {
	base := tempDir(t)
	a := makeRepo(t, filepath.Join(base, "a"), repoOptions{})
	b := makeRepo(t, filepath.Join(base, "b"), repoOptions{})
	expectPass(t, "cd "+b+" && git reset --hard", a)
	if !refExists(b, snapshotRef) || refExists(a, snapshotRef) {
		t.Error("only the cd target must be saved")
	}
}

// 破棄系が別々のリポジトリに 2 つあれば、どちらも保存してから通す。
func TestSnapshotMultipleTargetsAreAllSaved(t *testing.T) {
	base := tempDir(t)
	a := makeRepo(t, filepath.Join(base, "a"), repoOptions{})
	b := makeRepo(t, filepath.Join(base, "b"), repoOptions{})
	expectPass(t, "git reset --hard && cd "+b+" && git clean -fd", a)
	if got := show(t, a, snapshotRef+":tracked.txt"); got != "v2-uncommitted" {
		t.Errorf("a = %q", got)
	}
	if got := show(t, b, snapshotRef+":untracked.txt"); got != "new" {
		t.Errorf("b = %q", got)
	}
}

// git -C repoA -C sub は repoA/sub を保存する。cwd の側の sub（別のリポジトリ）には作らない。
func TestSnapshotMultipleDashCSavesTheRealTarget(t *testing.T) {
	base := tempDir(t)
	repoA := makeRepo(t, filepath.Join(base, "repoA"), repoOptions{})
	inner := makeRepo(t, filepath.Join(repoA, "sub"), repoOptions{})
	decoy := makeRepo(t, filepath.Join(base, "sub"), repoOptions{})
	expectPass(t, "git -C repoA -C sub reset --hard", base)
	if got := show(t, inner, snapshotRef+":tracked.txt"); got != "v2-uncommitted" {
		t.Errorf("repoA/sub tracked.txt = %q", got)
	}
	if refExists(decoy, snapshotRef) || refExists(repoA, snapshotRef) {
		t.Error("only repoA/sub must be saved")
	}
}

// (cd repoB && ...); git reset --hard は、サブシェルの外の cwd も保存する。
// 括弧を無視した作業ディレクトリ（repoB）も候補に残すので、repoB も保存する（余分に保存しても害はない）。
func TestSnapshotSubshellCdDoesNotHideTheOuterRepo(t *testing.T) {
	base := tempDir(t)
	a := makeRepo(t, filepath.Join(base, "a"), repoOptions{})
	b := makeRepo(t, filepath.Join(base, "b"), repoOptions{})
	expectPass(t, "(cd "+b+" && git status); git reset --hard", a)
	if got := show(t, a, snapshotRef+":tracked.txt"); got != "v2-uncommitted" {
		t.Errorf("a tracked.txt = %q", got)
	}
	if got := show(t, b, snapshotRef+":tracked.txt"); got != "v2-uncommitted" {
		t.Errorf("b tracked.txt = %q", got)
	}
}

// クォートの中の閉じ括弧はサブシェルを閉じないので、実際に破棄される cd の先を保存する。
func TestSnapshotQuotedParenthesisKeepsTheCdTarget(t *testing.T) {
	base := tempDir(t)
	a := makeRepo(t, filepath.Join(base, "a"), repoOptions{})
	b := makeRepo(t, filepath.Join(base, "b"), repoOptions{})
	expectPass(t, "(cd "+b+" && echo ':)' && git reset --hard)", a)
	if got := show(t, b, snapshotRef+":tracked.txt"); got != "v2-uncommitted" {
		t.Errorf("b tracked.txt = %q", got)
	}
}

// cd ~/repo の形でも対象を保存する。
func TestSnapshotTildeCdSavesTheTarget(t *testing.T) {
	home := tempDir(t)
	t.Setenv("HOME", home)
	repo := makeRepo(t, filepath.Join(home, "repo"), repoOptions{})
	elsewhere := mkdir(t, filepath.Join(home, "elsewhere"))
	expectPass(t, "cd ~/repo && git reset --hard", elsewhere)
	if got := show(t, repo, snapshotRef+":tracked.txt"); got != "v2-uncommitted" {
		t.Errorf("tracked.txt = %q", got)
	}
}

// 候補が同じ作業ツリーのサブディレクトリでも、保存は 1 回だけにする。
func TestSnapshotSameWorktreeIsSavedOnce(t *testing.T) {
	repo := tempRepo(t, repoOptions{})
	mkdir(t, filepath.Join(repo, "web"))
	expectPass(t, "git checkout -- a.go && cd web && git checkout -- b.ts && cd .. && git reset --hard", repo)
	if got := gitRun(t, repo, "reflog", "show", snapshotRef); len(strings.Split(got, "\n")) != 1 {
		t.Errorf("reflog = %q", got)
	}
}

func TestSnapshotTempIndexIsReused(t *testing.T) {
	repo := tempRepo(t, repoOptions{})
	expectPass(t, "git reset --hard", repo)
	index := filepath.Join(gitRun(t, repo, "rev-parse", "--absolute-git-dir"), snapshotIndexName)
	info, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	// mtime の分解能が粗いファイルシステムでも差が出るように、時刻を戻しておく。
	past := info.ModTime().Add(-time.Hour)
	if err := os.Chtimes(index, past, past); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "another.txt"), "y\n")
	expectPass(t, "git reset --hard", repo)
	if info, err := os.Stat(index); err != nil || info.ModTime().Equal(past) {
		t.Errorf("temp index must be rewritten: %v", err)
	}
	if got := show(t, repo, snapshotRef+":another.txt"); got != "y" {
		t.Errorf("another.txt = %q", got)
	}
}

// symlink 経由のパスでも保存できる（~/dotfiles のような構成）。
func TestSnapshotSymlinkedRepoPath(t *testing.T) {
	repo := tempRepo(t, repoOptions{})
	link := filepath.Join(filepath.Dir(repo), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	expectPass(t, "git reset --hard", link)
	if !refExists(repo, snapshotRef) {
		t.Error("snapshot must be created through the symlink")
	}
}

// cwd に空白を含むパスが来ても扱える（シェルを経由しないため）。
func TestSnapshotRepoPathWithSpace(t *testing.T) {
	base := mkdir(t, filepath.Join(tempDir(t), "snapshot test"))
	repo := makeRepo(t, filepath.Join(base, "my repo"), repoOptions{})
	expectPass(t, "git reset --hard", repo)
	if got := show(t, repo, snapshotRef+":tracked.txt"); got != "v2-uncommitted" {
		t.Errorf("tracked.txt = %q", got)
	}
}

// 並行して実行しても壊れない（一時 index の競合では deny に落ちる）。
func TestSnapshotParallelInvocations(t *testing.T) {
	repo := tempRepo(t, repoOptions{})
	definition := Definition()
	outputs := make([]bytes.Buffer, 2)
	var group sync.WaitGroup
	for index := range outputs {
		group.Go(func() {
			hookrt.Run(&definition, hookrt.Invocation{
				Stdin:      strings.NewReader(hooktest.BashPayload("git reset --hard", repo, nil)),
				Stdout:     &outputs[index],
				LoadConfig: func() (*config.Config, error) { return nil, nil },
			})
		})
	}
	group.Wait()
	passed := 0
	for index := range outputs {
		output := outputs[index].String()
		switch {
		case output == "":
			passed++
		// 設定を渡さないので英語の理由文になる。
		case !strings.Contains(output, `"deny"`) || !strings.Contains(output, reasonMarker(denyFailed(i18n.English, "x"))):
			t.Errorf("unexpected output: %q", output)
		}
	}
	if passed == len(outputs) && !refExists(repo, snapshotRef) {
		t.Error("both passed but no snapshot exists")
	}
}

// argv の経路では cwd が渡らないので、プロセスの作業ディレクトリ（git 管理下でない）を対象にする。
func TestSnapshotArgvDebugPath(t *testing.T) {
	got := hooktest.Argv(t, Definition(), "git reset --hard")
	if got.Decision != hooktest.Deny || !strings.Contains(got.Reason, failedMark) {
		t.Errorf("got %+v", got)
	}
	repo := tempRepo(t, repoOptions{})
	t.Chdir(repo)
	if got := hooktest.Argv(t, Definition(), "git reset --hard"); got.Decision != "" {
		t.Errorf("argv in a repository must pass: %+v", got)
	}
	if !refExists(repo, snapshotRef) {
		t.Error("argv path must save the process working directory")
	}
}

// 持ち時間を使い切ったら、git の失敗として deny に落とす（agent 側のタイムアウトで無言に落ちない）。
func TestTimeBudgetFallsToDeny(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep is not available")
	}
	bin := mkdir(t, filepath.Join(tempDir(t), "bin"))
	writeFile(t, filepath.Join(bin, "git"), "#!/bin/sh\nexec "+sleep+" 10\n")
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	savedBudget, savedTimeout := timeBudget, gitTimeout
	timeBudget, gitTimeout = 300*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { timeBudget, gitTimeout = savedBudget, savedTimeout })

	start := time.Now()
	expectDeny(t, "git reset --hard", tempDir(t), failedMark)
	// git は 1 回目の起動で時間切れになる。すぐ終わったなら、時間切れではなく起動の失敗で deny になっている。
	if elapsed := time.Since(start); elapsed < gitTimeout || elapsed > 5*time.Second {
		t.Errorf("took %v", elapsed)
	}
	// git が見つからなくても失敗として deny する。
	t.Setenv("PATH", t.TempDir())
	expectDeny(t, "git reset --hard", tempDir(t), failedMark)
}

// 時間切れの git は SIGTERM で止め、git が lock を消せるようにする。SIGKILL だと一時 index の lock が残り、
// 以後その作業ツリーの破棄系のコマンドがすべて deny になる。
func TestTimeoutLetsGitRemoveItsLock(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep is not available")
	}
	rm, err := exec.LookPath("rm")
	if err != nil {
		t.Skip("rm is not available")
	}
	lock := filepath.Join(tempDir(t), "index.lock")
	bin := mkdir(t, filepath.Join(tempDir(t), "bin"))
	script := "#!/bin/sh\n" +
		"trap '" + rm + " -f \"" + lock + "\"; exit 143' TERM\n" +
		": > \"" + lock + "\"\n" +
		"while :; do " + sleep + " 0.05; done\n"
	writeFile(t, filepath.Join(bin, "git"), script)
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	savedBudget, savedTimeout := timeBudget, gitTimeout
	timeBudget, gitTimeout = 500*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { timeBudget, gitTimeout = savedBudget, savedTimeout })

	expectDeny(t, "git reset --hard", tempDir(t), failedMark)
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Errorf("lock must be removed by the terminated git: %v", err)
	}
}

// --- L3: git worktree の環境。「どの作業ツリーを保存したか」を中身で確かめる -----------
//
// git は cwd / -C / --git-dir / --work-tree / GIT_DIR のどの指定でも基準が変わる。取り違えると
// 「無関係な作業ツリーを保存し、本来の対象は無防備なまま破棄される」という、この hook で最悪の壊れ方になる。
// 構成: main（メインの作業ツリー）と wt1 / wt2（linked worktree）。HEAD の内容は同じで、未コミットの変更だけが違う。

type worktrees struct {
	base, main, wt1, wt2 string
}

func newWorktrees(t *testing.T) worktrees {
	t.Helper()
	base := tempDir(t)
	env := worktrees{base: base, main: makeRepo(t, filepath.Join(base, "main"), repoOptions{})}
	gitRun(t, env.main, "add", "-A")
	gitRun(t, env.main, "commit", "-qm", "base") // 作業ツリーを一度きれいにする
	env.wt1 = filepath.Join(base, "wt1")
	env.wt2 = filepath.Join(base, "wt2")
	gitRun(t, env.main, "worktree", "add", "-q", "-b", "topic1", env.wt1)
	gitRun(t, env.main, "worktree", "add", "-q", "-b", "topic2", env.wt2)
	for name, path := range map[string]string{"main": env.main, "wt1": env.wt1, "wt2": env.wt2} {
		writeFile(t, filepath.Join(path, "tracked.txt"), name+"-uncommitted\n")
		writeFile(t, filepath.Join(path, "only-in-"+name+".txt"), name)
	}
	return env
}

func (w worktrees) path(name string) string {
	return map[string]string{"main": w.main, "wt1": w.wt1, "wt2": w.wt2}[name]
}

// assertSnapshotOf は ref の中身が name の作業ツリーであることを確かめる。ref は作業ツリーの間で共有されるので、どこから読んでも同じである。
// メッセージの "@ <toplevel>" は --show-toplevel の値（symlink を解決済み）なので、realpath で比べる。
func (w worktrees) assertSnapshotOf(t *testing.T, name, ref string) {
	t.Helper()
	if got := show(t, w.main, ref+":tracked.txt"); got != name+"-uncommitted" {
		t.Errorf("%s tracked.txt = %q, want %s", ref, got, name)
	}
	if got := show(t, w.main, ref+":only-in-"+name+".txt"); got != name {
		t.Errorf("%s only-in-%s.txt = %q", ref, name, got)
	}
	if subject := gitRun(t, w.main, "log", "-1", "--format=%s", ref); !strings.HasSuffix(subject, "@ "+realpath(t, w.path(name))) {
		t.Errorf("%s subject %q does not name %s", ref, subject, name)
	}
}

func TestWorktreeCWDIsALinkedWorktree(t *testing.T) {
	w := newWorktrees(t)
	expectPass(t, "git reset --hard", w.wt1)
	w.assertSnapshotOf(t, "wt1", snapshotRef)
}

func TestWorktreeCWDIsTheMainWorktree(t *testing.T) {
	w := newWorktrees(t)
	expectPass(t, "git clean -fd", w.main)
	w.assertSnapshotOf(t, "main", snapshotRef)
}

func TestWorktreeDashCIntoLinkedWorktree(t *testing.T) {
	w := newWorktrees(t)
	expectPass(t, "git -C "+w.wt1+" reset --hard", w.main)
	w.assertSnapshotOf(t, "wt1", snapshotRef)
}

func TestWorktreeDashCIntoMainWorktree(t *testing.T) {
	w := newWorktrees(t)
	expectPass(t, "git -C "+w.main+" checkout -- .", w.wt1)
	w.assertSnapshotOf(t, "main", snapshotRef)
}

func TestWorktreeRelativeDashCFromWorktree(t *testing.T) {
	w := newWorktrees(t)
	expectPass(t, "git -C ../main reset --hard", w.wt1)
	w.assertSnapshotOf(t, "main", snapshotRef)
}

func TestWorktreeCdToAnotherWorktree(t *testing.T) {
	w := newWorktrees(t)
	expectPass(t, "cd "+w.wt2+" && git restore .", w.wt1)
	w.assertSnapshotOf(t, "wt2", snapshotRef)
}

// 作業ツリーの中のサブディレクトリからでも、その作業ツリー全体を保存する。
func TestWorktreeSubdirectoryInsideWorktree(t *testing.T) {
	w := newWorktrees(t)
	sub := mkdir(t, filepath.Join(w.wt1, "sub"))
	writeFile(t, filepath.Join(sub, "x.txt"), "x")
	expectPass(t, "git reset --hard", sub)
	w.assertSnapshotOf(t, "wt1", snapshotRef)
	if got := show(t, w.wt1, snapshotRef+":sub/x.txt"); got != "x" {
		t.Errorf("sub/x.txt = %q", got)
	}
}

// -C がサブディレクトリを指しても、その作業ツリーの toplevel まで登る。
func TestWorktreeDashCIntoSubdirectoryOfAnotherWorktree(t *testing.T) {
	w := newWorktrees(t)
	sub := mkdir(t, filepath.Join(w.wt2, "deep", "nested"))
	writeFile(t, filepath.Join(sub, "y.txt"), "y")
	expectPass(t, "git -C "+sub+" reset --hard", w.wt1)
	w.assertSnapshotOf(t, "wt2", snapshotRef)
	if got := show(t, w.wt2, snapshotRef+":deep/nested/y.txt"); got != "y" {
		t.Errorf("deep/nested/y.txt = %q", got)
	}
}

// メインの作業ツリーの中に worktree を作った構成でも、メインの側を保存できる。
func TestWorktreeInsideTheMainWorktree(t *testing.T) {
	w := newWorktrees(t)
	gitRun(t, w.main, "worktree", "add", "-q", "-b", "topic-inside", filepath.Join(w.main, "inside-wt"))
	expectPass(t, "git reset --hard", w.main)
	w.assertSnapshotOf(t, "main", snapshotRef)
}

// 一時 index は作業ツリーごとの git ディレクトリに置く。共有すると stat cache が混線し、別の作業ツリーの状態を保存しかねない。
func TestWorktreeTempIndexIsPerWorktree(t *testing.T) {
	w := newWorktrees(t)
	expectPass(t, "git reset --hard", w.wt1)
	w.assertSnapshotOf(t, "wt1", snapshotRef)
	expectPass(t, "git reset --hard", w.main)
	w.assertSnapshotOf(t, "main", snapshotRef)

	gitDirs := map[string]string{}
	seen := map[string]bool{}
	for _, name := range []string{"main", "wt1", "wt2"} {
		gitDirs[name] = gitRun(t, w.path(name), "rev-parse", "--absolute-git-dir")
		seen[gitDirs[name]] = true
	}
	if len(seen) != 3 {
		t.Fatalf("git directories must differ: %v", gitDirs)
	}
	for _, name := range []string{"main", "wt1"} {
		if _, err := os.Stat(filepath.Join(gitDirs[name], snapshotIndexName)); err != nil {
			t.Errorf("%s has no temp index: %v", name, err)
		}
	}
	// 触っていない wt2 には作らない。
	if _, err := os.Stat(filepath.Join(gitDirs["wt2"], snapshotIndexName)); !os.IsNotExist(err) {
		t.Errorf("wt2 must not have a temp index: %v", err)
	}
}

// 既知の挙動: ref は worktree の間で共有される。判別はメッセージの "@ <toplevel>" で行う。
func TestWorktreeRefIsSharedButReflogIdentifiesTheWorktree(t *testing.T) {
	w := newWorktrees(t)
	expectPass(t, "git reset --hard", w.wt1)
	first := gitRun(t, w.wt1, "rev-parse", snapshotRef)
	expectPass(t, "git clean -fd", w.wt2)

	// 先頭は後から破棄した wt2 のものである。
	if gitRun(t, w.wt2, "rev-parse", snapshotRef) == first {
		t.Error("the ref must move to wt2's snapshot")
	}
	w.assertSnapshotOf(t, "wt2", snapshotRef)
	// wt1 のものは reflog から辿れる。
	if got := gitRun(t, w.main, "rev-parse", snapshotRef+"@{1}"); got != first {
		t.Errorf("@{1} = %q, want %q", got, first)
	}
	w.assertSnapshotOf(t, "wt1", snapshotRef+"@{1}")

	lines := strings.Split(gitRun(t, w.main, "reflog", "show", snapshotRef), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "@ "+realpath(t, w.wt2)) || !strings.Contains(lines[1], "@ "+realpath(t, w.wt1)) {
		t.Errorf("reflog = %q", lines)
	}
}

// 親のコミットはその作業ツリーの HEAD である（別のブランチをチェックアウトしている）。
func TestWorktreeParentIsTheWorktreeHead(t *testing.T) {
	w := newWorktrees(t)
	expectPass(t, "git reset --hard", w.wt1)
	if got, want := gitRun(t, w.main, "rev-parse", snapshotRef+"^"), gitRun(t, w.wt1, "rev-parse", "HEAD"); got != want {
		t.Errorf("parent = %q, want %q", got, want)
	}
	if got := gitRun(t, w.wt1, "rev-parse", "--abbrev-ref", "HEAD"); got != "topic1" {
		t.Errorf("wt1 HEAD = %q", got)
	}
}

// --git-dir / --work-tree / GIT_DIR は基準が変わるので、特定せずに deny する。
func TestWorktreeBaseOverridesFallToDeny(t *testing.T) {
	w := newWorktrees(t)
	gitDir := gitRun(t, w.wt1, "rev-parse", "--absolute-git-dir")
	for _, command := range []string{
		"git --git-dir=" + gitDir + " --work-tree=" + w.wt1 + " reset --hard",
		"GIT_DIR=" + gitDir + " GIT_WORK_TREE=" + w.wt1 + " git reset --hard",
		"GIT_WORK_TREE=" + w.wt1 + " git clean -fd",
	} {
		expectDeny(t, command, w.main, unresolved)
	}
	if refExists(w.main, snapshotRef) {
		t.Error("denied commands must not create a snapshot")
	}
}

// 2 つの作業ツリーを同時に破棄する形は、両方を保存してから通す。
func TestWorktreeTwoWorktreesAreBothSaved(t *testing.T) {
	w := newWorktrees(t)
	expectPass(t, "git -C "+w.wt1+" reset --hard && git -C "+w.wt2+" clean -fd", w.main)
	// 出現順に保存するので、ref の先頭は後から保存した wt2 で、wt1 は 1 つ前にある。
	w.assertSnapshotOf(t, "wt2", snapshotRef)
	w.assertSnapshotOf(t, "wt1", snapshotRef+"@{1}")
	if got := gitRun(t, w.main, "reflog", "show", snapshotRef); len(strings.Split(got, "\n")) != 2 {
		t.Errorf("reflog = %q", got)
	}
}

// ヒアドキュメントで書いたスクリプトの中に cd が複数あっても、破棄した時点の作業ツリーを保存する。
func TestWorktreeScriptWithSeveralCdsIntoWorktree(t *testing.T) {
	w := newWorktrees(t)
	mkdir(t, filepath.Join(w.wt1, "web"))
	command := "cat > run.sh <<'EOF'\n#!/bin/zsh\ncd " + w.wt1 + " || exit 1\n" +
		"f=internal/doc.go\necho \"// touch\" >> \"$f\"\ngo test ./...\n" +
		"git checkout -- \"$f\"\ncd web\npnpm test\ncd ..\nEOF\nchmod +x run.sh && ./run.sh"
	expectPass(t, command, w.main)
	w.assertSnapshotOf(t, "wt1", snapshotRef)
	if got := gitRun(t, w.main, "reflog", "show", snapshotRef); len(strings.Split(got, "\n")) != 1 {
		t.Errorf("reflog = %q", got)
	}
}

// bare のクローンに worktree を足した構成（メインの作業ツリーが無い）でも保存できる。
func TestWorktreeOfBareRepo(t *testing.T) {
	w := newWorktrees(t)
	bare := filepath.Join(w.base, "bare.git")
	gitRun(t, w.base, "clone", "--bare", "-q", w.main, bare)
	wt := filepath.Join(w.base, "from-bare")
	gitRun(t, bare, "worktree", "add", "-q", "-b", "topic-bare", wt)
	writeFile(t, filepath.Join(wt, "tracked.txt"), "bare-uncommitted\n")
	writeFile(t, filepath.Join(wt, "only-in-bare.txt"), "bare")

	expectPass(t, "git reset --hard", wt)
	if got := show(t, wt, snapshotRef+":tracked.txt"); got != "bare-uncommitted" {
		t.Errorf("tracked.txt = %q", got)
	}
	if got := show(t, wt, snapshotRef+":only-in-bare.txt"); got != "bare" {
		t.Errorf("only-in-bare.txt = %q", got)
	}
	// ref は共通ディレクトリ（bare の側）に置かれ、元のリポジトリには作られない。
	if !refExists(bare, snapshotRef) || refExists(w.main, snapshotRef) {
		t.Error("the ref must live in the bare repository only")
	}
	if subject := gitRun(t, wt, "log", "-1", "--format=%s", snapshotRef); !strings.HasSuffix(subject, "@ "+realpath(t, wt)) {
		t.Errorf("subject = %q", subject)
	}
}

// 作ったばかりの worktree（未コミットが未追跡のファイルだけ）でも保存できる。
func TestWorktreeAddedThenImmediatelyDiscarded(t *testing.T) {
	w := newWorktrees(t)
	fresh := filepath.Join(w.base, "fresh")
	gitRun(t, w.main, "worktree", "add", "-q", "-b", "topic3", fresh)
	writeFile(t, filepath.Join(fresh, "generated.txt"), "generated\n")
	expectPass(t, "git clean -fd", fresh)
	if got := show(t, fresh, snapshotRef+":generated.txt"); got != "generated" {
		t.Errorf("generated.txt = %q", got)
	}
}

// 作業ツリーがきれいなら ref を作らない（他の作業ツリーが汚れていても）。
func TestWorktreeCleanWorktreeCreatesNoRef(t *testing.T) {
	w := newWorktrees(t)
	clean := filepath.Join(w.base, "cleanwt")
	gitRun(t, w.main, "worktree", "add", "-q", "-b", "topic4", clean)
	expectPass(t, "git reset --hard", clean)
	if refExists(w.main, snapshotRef) {
		t.Error("clean worktree must not create a snapshot")
	}
}

// L2: 作業ツリーを指す各種の書き方が、対象のディレクトリまで解決できる。
func TestWorktreePathsAreResolvable(t *testing.T) {
	w := newWorktrees(t)
	for _, testCase := range []struct {
		command, cwd string
		want         []string
	}{
		{"git reset --hard", w.wt1, []string{w.wt1}},
		{"git -C " + w.wt1 + " reset --hard", w.main, []string{w.wt1}},
		{"cd " + w.wt2 + " && git reset --hard", w.wt1, []string{w.wt2}},
		{"git -C ../wt2 reset --hard", w.wt1, []string{w.wt2}},
		{"cd ../main && git reset --hard", w.wt1, []string{w.main}},
		{"cd ../main && cd ../wt2 && git reset --hard", w.wt1, []string{w.wt2}},
		{"git reset --hard && git -C " + w.wt2 + " clean -fd", w.wt1, []string{w.wt1, w.wt2}},
	} {
		assertTargets(t, testCase.command, testCase.cwd, testCase.want...)
	}
}

// --- scratchpad と同じコマンドの中の変数 -------------------------------------

// testSession は hooktest.BashPayload が payload に入れる session_id である。
const testSession = "test-session"

type scratchFixture struct {
	// root は scratchRoot の実体、sp はこのセッションの scratchpad、other は別のセッションの scratchpad である。
	root, sp, other string
	// repo は scratchpad の外の、未コミットの変更があるリポジトリである。
	repo string
	// clone は scratchpad の中の、未コミットの変更がある clone である。
	clone string
}

// newScratchFixture は scratchRoot を一時ディレクトリに差し替え、Claude Code と同じ形の scratchpad を作る。
func newScratchFixture(t *testing.T) scratchFixture {
	t.Helper()
	tmp := tempDir(t)
	root := mkdir(t, filepath.Join(tmp, "tmproot"))
	previous := scratchRoot
	scratchRoot = root
	t.Cleanup(func() { scratchRoot = previous })
	user := filepath.Join(root, "claude-"+strconv.Itoa(os.Getuid()), "-Users-alice-dev-acme")
	f := scratchFixture{
		root:  root,
		sp:    mkdir(t, filepath.Join(user, testSession, "scratchpad")),
		other: mkdir(t, filepath.Join(user, "other-session", "scratchpad")),
		repo:  makeRepo(t, filepath.Join(tmp, "repo"), repoOptions{}),
	}
	f.clone = makeRepo(t, filepath.Join(f.sp, "tgv1"), repoOptions{})
	return f
}

func codexPayload(command, cwd string) string {
	data, err := json.Marshal(map[string]any{
		"session_id":      testSession,
		"transcript_path": "/Users/alice/.codex/sessions/2026/10/25/rollout-2026-10-25T12-00-00-" + testSession + ".jsonl",
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"tool_input":      map[string]any{"command": command},
		"cwd":             cwd,
	})
	if err != nil {
		panic(err)
	}
	return string(data)
}

// 使い捨ての clone を同じコマンドで作って破棄する形は、snapshot を取らずに通す（clone はまだ無い）。
func TestScratchpadCloneCreatedInTheSameCommandPasses(t *testing.T) {
	f := newScratchFixture(t)
	home := mkdir(t, filepath.Join(f.root, "home"))
	t.Setenv("HOME", home)
	source := makeRepo(t, filepath.Join(home, "src"), repoOptions{})
	command := "set -e; S=" + f.sp + "; D=$S/verify1; cd ~/src; git clone -q --shared ~/src $D; git -C $D checkout -q -- ."
	checkProtected(t, f.repo, command)
	if refExists(source, snapshotRef) || refExists(f.repo, snapshotRef) {
		t.Error("a discard inside the scratchpad must not take a snapshot")
	}
}

// 既にある scratchpad の clone を変数で指して破棄する形も、snapshot を取らずに通す。
func TestScratchpadExistingCloneViaVariablePasses(t *testing.T) {
	f := newScratchFixture(t)
	for _, command := range []string{
		"D=" + f.clone + "; cd $D && git reset --hard HEAD",
		`D="` + f.clone + `"; cd "$D" && git reset --hard`,
		"S=" + f.sp + "; git -C ${S}/tgv1 reset --hard",
		"export D=" + f.clone + "; git -C $D clean -fd",
		"S=" + f.sp + "\nD=$S/tgv1\ngit -C $D checkout -- .",
		"git -C " + f.clone + " reset --hard",
		"cd " + f.clone + " && git reset --hard",
	} {
		checkProtected(t, f.repo, command)
	}
	if refExists(f.clone, snapshotRef) || refExists(f.repo, snapshotRef) {
		t.Error("a discard inside the scratchpad must not take a snapshot")
	}
}

// scratchpad へスクリプトを書き出すだけの here-document は、本文の破棄を検出しない。
func TestScratchpadHeredocWrittenToAFilePasses(t *testing.T) {
	f := newScratchFixture(t)
	for _, command := range []string{
		"cat > " + f.sp + "/mkclone.sh <<'EOF'\n#!/bin/bash\nset -e\nDST=$1\ngit clone -q --shared \"$SRC\" \"$DST\"\n" +
			"cd \"$DST\"\ngit checkout -q -- .\nEOF",
		"S=" + f.sp + "; cat > $S/mkclone.sh <<'EOF'\ncd \"$DST\"\ngit checkout -q -- .\nEOF\nchmod +x $S/other.sh",
		"tee " + f.sp + "/run.sh <<EOF >/dev/null\ncd $DST && git reset --hard\nEOF",
	} {
		expectPass(t, command, f.repo)
	}
	if refExists(f.repo, snapshotRef) {
		t.Error("a script written to a file must not take a snapshot")
	}
}

// 「deny のまま残る実例」。どれも対象を特定できない理由で止める。
func TestScratchpadUnresolvedFormsStillDeny(t *testing.T) {
	f := newScratchFixture(t)
	for _, command := range []string{
		// 同じコマンドに代入が無い。
		"cd $REPO && git reset --hard",
		// 別のセッションの scratchpad。
		"D=" + f.other + "/x; git -C $D reset --hard",
		// シェルへ流し込む本文。内側のシェルは外側の変数を見ない。
		"D=" + f.clone + "; bash <<'EOF'\ncd $D\ngit reset --hard\nEOF",
		"bash <<'EOF'\nD=" + f.clone + "\ncd $D\ngit reset --hard\nEOF",
		"cat <<'EOF' | sh\ncd $D\ngit reset --hard\nEOF",
		// 書き出したスクリプトを同じコマンドで実行する。
		"cat > " + f.sp + "/run.sh <<'EOF'\ncd \"$1\"\ngit reset --hard\nEOF\nbash " + f.sp + "/run.sh x",
	} {
		expectDeny(t, command, f.repo, unresolved)
	}
	if refExists(f.repo, snapshotRef) || refExists(f.clone, snapshotRef) {
		t.Error("denied commands must not create a snapshot")
	}
}

// 変数を展開した先が scratchpad の外なら、リテラルのパスと同じく snapshot を取ってから通す。
func TestScratchpadVariableOutsideTakesASnapshot(t *testing.T) {
	f := newScratchFixture(t)
	checkProtected(t, f.clone, "D="+f.repo+"; git -C $D reset --hard")
	if !refExists(f.repo, snapshotRef) {
		t.Error("the repository outside the scratchpad must be saved")
	}
}

// 値が変わりうる変数は信用しない。
func TestScratchpadUntrustedVariablesDeny(t *testing.T) {
	f := newScratchFixture(t)
	for _, command := range []string{
		"D=" + f.repo + "; [ -d x ] && D=" + f.clone + "; git -C $D reset --hard", // 2 回代入する
		"true && D=" + f.clone + "; git -C $D reset --hard",                       // 条件つきの代入
		"false || D=" + f.clone + "; git -C $D reset --hard",
		"if true; then\nD=" + f.clone + "\nfi\ngit -C $D reset --hard",
		"{ D=" + f.clone + "; }; git -C $D reset --hard",
		"D=" + f.clone + " git -C $D reset --hard", // git の環境だけを変える
		"D=" + f.clone + " | true; git -C $D reset --hard",
		"D=" + f.clone + " & git -C $D reset --hard",
		"for D in " + f.clone + "; do git -C $D reset --hard; done",
		"(D=" + f.clone + "); git -C $D reset --hard", // サブシェルの中の代入
		"D=" + f.clone + "; eval \"$X\"; git -C $D reset --hard",
		"D=" + f.clone + "; source ./env.sh; git -C $D reset --hard",
		"D=" + f.clone + "; read D; git -C $D reset --hard",
		"D=" + f.clone + "; : ${D:=x}; git -C $D reset --hard",
		"D=" + f.clone + "; D+=/x; git -C $D reset --hard",
		"D=$(echo " + f.clone + "); git -C $D reset --hard",
		"D=`echo " + f.clone + "`; git -C $D reset --hard",
		"D=$HOME; git -C $D reset --hard",
		"D=; git -C \"$D\" reset --hard",               // 空の値
		"D='" + f.sp + "/a b'; git -C $D reset --hard", // 分割される値
		"D=" + f.sp + "/*; git -C $D reset --hard",
		"D=" + f.clone + "; git -C \"$D/$X\" reset --hard",
		"D=" + f.clone + "; git -C $1 reset --hard",
		"D=" + f.clone + "; GIT_DIR=$D/.git; git reset --hard",
	} {
		expectDeny(t, command, f.repo, unresolved)
	}
	if refExists(f.repo, snapshotRef) || refExists(f.clone, snapshotRef) {
		t.Error("denied commands must not create a snapshot")
	}
}

// まだ無い scratchpad のディレクトリへの cd は、失敗すれば前のディレクトリに留まる。
// && でつながるか、set -e の下で単独で走る場合だけ通す。
func TestScratchpadCdIntoMissingDirectory(t *testing.T) {
	f := newScratchFixture(t)
	missing := f.sp + "/new"
	for _, command := range []string{
		"git clone -q " + f.repo + " " + missing + " && cd " + missing + " && git reset --hard",
		"git clone -q " + f.repo + " " + missing + " && cd " + missing + " &&\ngit reset --hard",
		"set -e; git clone -q " + f.repo + " " + missing + "; cd " + missing + "; git checkout -B x",
		"set -euo pipefail\nW=" + missing + "\ngit clone -q " + f.repo + " $W\ncd $W\ngit reset --hard",
		"git -C " + missing + " reset --hard", // git -C は行き先が無ければ失敗する
		"cd " + missing + " && (git reset --hard)",
		// 絶対パスの cd は、前の失敗したかもしれない cd に依らない。
		"cd " + f.sp + "/a; git clone -q " + f.repo + " " + missing + " && cd " + missing + " && git reset --hard",
	} {
		checkProtected(t, f.repo, command)
	}
	for _, command := range []string{
		"cd " + missing + "; git reset --hard",
		"cd " + missing + "\ngit reset --hard",
		"set -e; cd " + missing + " || true; git reset --hard",
		"set -e; true || cd " + missing + "; git reset --hard",
		"set -e; if true; then cd " + missing + "; fi; git reset --hard",
		"set -e; set +e; cd " + missing + "; git reset --hard",
		"cd " + missing + " && true; git reset --hard",
		"true || cd " + missing + " && git reset --hard",
		"cd " + missing + " && git -C sub reset --hard; git reset --hard",
		"cd " + missing + " | git reset --hard",
		// 相対パスの cd は、前の cd が失敗していれば別の場所（f.repo/sub）を指す。
		"set -e; cd " + missing + " && true; cd sub; git reset --hard",
		"cd " + missing + "; cd sub && git reset --hard",
	} {
		expectDeny(t, command, f.repo, unresolved)
	}
	if refExists(f.repo, snapshotRef) {
		t.Error("the commands above must not create a snapshot")
	}
	// 絶対パスの -C は、失敗したかもしれない cd に依らない。
	expectPass(t, "cd "+missing+"; git -C "+f.repo+" reset --hard", f.clone)
	if !refExists(f.repo, snapshotRef) {
		t.Error("the absolute -C target must be saved")
	}
}

// scratchpad の中の symlink が外のリポジトリを指していれば、外として扱う。同じコマンドで symlink を作る形は特定できないものとする。
func TestScratchpadSymlinkToOutside(t *testing.T) {
	f := newScratchFixture(t)
	link := filepath.Join(f.sp, "link")
	if err := os.Symlink(f.repo, link); err != nil {
		t.Fatal(err)
	}
	checkProtected(t, f.clone, "git -C "+link+" reset --hard")
	if !refExists(f.repo, snapshotRef) {
		t.Error("the repository behind the symlink must be saved")
	}
	expectDeny(t, "ln -s "+f.repo+" "+f.sp+"/new && git -C "+f.sp+"/new reset --hard", f.clone, unresolved)
	expectDeny(t, "git -C "+f.sp+"/../../other-session/scratchpad/x reset --hard", f.clone, unresolved)
}

// scratchpad の中の git ディレクトリでも、作業ツリーが外にあれば snapshot の対象にする（通さずに保存を試みる）。
func TestScratchpadWorktreeOutsideIsNotExempt(t *testing.T) {
	f := newScratchFixture(t)
	gitRun(t, f.clone, "config", "core.worktree", f.repo)
	// snapshot は作業ツリー（rev-parse --show-toplevel の先）で取る。
	got := runHook(t, "git -C "+f.clone+" reset --hard", f.clone)
	if got.Decision == "" && !refExists(f.repo, snapshotRef) {
		t.Errorf("a work tree outside the scratchpad must not pass without a snapshot: %+v", got)
	}
}

// /tmp と /private/tmp のような symlink の別名でも、同じ scratchpad として扱う。
func TestScratchpadRootSymlink(t *testing.T) {
	f := newScratchFixture(t)
	alias := filepath.Join(filepath.Dir(f.root), "tmpalias")
	if err := os.Symlink(f.root, alias); err != nil {
		t.Fatal(err)
	}
	scratchRoot = alias
	aliased := strings.Replace(f.clone, f.root, alias, 1)
	for _, command := range []string{
		"git -C " + aliased + " reset --hard",
		"git -C " + f.clone + " reset --hard",
	} {
		checkProtected(t, f.repo, command)
	}
	if refExists(f.clone, snapshotRef) {
		t.Error("a discard inside the scratchpad must not take a snapshot")
	}
}

// Codex とデバッグ経路は scratchpad の判定をしない（変数の展開はする）。
func TestScratchpadIsClaudeOnly(t *testing.T) {
	f := newScratchFixture(t)
	command := "D=" + f.clone + "; git -C $D reset --hard"
	if got := hooktest.Stdin(t, Definition(), codexPayload(command, f.repo)); got.Decision != "" {
		t.Errorf("codex: %+v", got)
	}
	if !refExists(f.clone, snapshotRef) {
		t.Error("codex must save the clone")
	}
	before := gitRun(t, f.clone, "rev-parse", snapshotRef)
	writeFile(t, filepath.Join(f.clone, "tracked.txt"), "v3\n")
	if got := hooktest.Argv(t, Definition(), command); got.Decision != "" {
		t.Errorf("argv: %+v", got)
	}
	if after := gitRun(t, f.clone, "rev-parse", snapshotRef); after == before {
		t.Error("argv must save the clone")
	}
}

func TestNewScratchpad(t *testing.T) {
	for _, testCase := range []struct {
		session, transcript string
		want                bool
	}{
		{testSession, "/Users/alice/.claude/projects/x/" + testSession + ".jsonl", true},
		{"", "/dev/null", false},
		{"..", "/dev/null", false},
		{"a/b", "/dev/null", false},
		{testSession, "/Users/alice/.codex/sessions/rollout-2026-" + testSession + ".jsonl", false},
	} {
		if got := newScratchpad(testCase.session, testCase.transcript).session != ""; got != testCase.want {
			t.Errorf("newScratchpad(%q, %q) = %v", testCase.session, testCase.transcript, got)
		}
	}
	f := newScratchFixture(t)
	s := newScratchpad(testSession, "/dev/null")
	for path, want := range map[string]bool{
		f.sp:                      true,
		f.sp + "/x/y":             true,
		f.sp + "/../scratchpad/x": true,
		f.sp + "x":                false,
		f.sp + "/..":              false,
		f.other + "/x":            false,
		filepath.Dir(f.sp):        false,
		f.repo:                    false,
		strings.Replace(f.sp, "claude-", "claude-x", 1): false,
	} {
		if got := s.contains(path); got != want {
			t.Errorf("contains(%q) = %v, want %v", path, got, want)
		}
	}
	if (scratchpad{}).contains(f.sp) {
		t.Error("the zero scratchpad must contain nothing")
	}
}

// --- L2: 変数と here-document の部品 -----------------------------------------

func TestResolveExpandsVariablesAssignedInTheCommand(t *testing.T) {
	f := newResolveFixture(t)
	sub := filepath.Join(f.a, "sub")
	t.Setenv("HOME", f.tmp)
	assertTargets(t, "D="+f.b+"; git -C $D reset --hard", f.a, f.b)
	assertTargets(t, "S="+f.a+"; D=$S/sub; cd $D && git reset --hard", f.b, sub)
	assertTargets(t, "S="+f.a+" D=${S}/sub; git -C \"$D\" clean -fd", f.b, sub)
	assertTargets(t, "R=~/a; git -C $R reset --hard", f.b, f.a)
	assertTargets(t, "R=sub; git -C $R reset --hard", f.a, sub)
	assertTargets(t, "R=sub; cd $R; cd ..; git -C ./$R reset --hard", f.a, sub)
	assertTargets(t, "R='"+f.b+"'; git -C $R reset --hard", f.a, f.b)
	assertTargets(t, "git -C '"+f.b+"' reset --hard", f.a, f.b)
	// クォートの中の空白や区切り文字で分かれた断片をつなぐと元のパスと違いうるので、別のディレクトリ（a b）があっても追わない。
	mkdir(t, filepath.Join(f.a, "a b"))
	for _, command := range []string{
		"git -C 'a  b' reset --hard",
		`cd "a;b" && git reset --hard`,
		"cd 'a　b' && git reset --hard",
		"D='a  b'; git -C \"$D\" reset --hard",
	} {
		if got, err := resolveTargetDirs(command, f.a); got != nil || err != nil {
			t.Errorf("%q must be unresolved: %v, %v", command, got, err)
		}
	}
	// 使う時点でまだ代入していない変数や、値が変わりうる変数は追わない。
	for _, command := range []string{
		"git -C $D reset --hard; D=" + f.b,
		"D=" + f.b + "; D=" + f.a + "; git -C $D reset --hard",
		"D=" + f.b + "\nfunction f { D=" + f.a + "; }\nf\ngit -C $D reset --hard",
	} {
		if got, err := resolveTargetDirs(command, f.a); got != nil || err != nil {
			t.Errorf("%q must be unresolved: %v, %v", command, got, err)
		}
	}
}

func TestTrustedNames(t *testing.T) {
	// パスやフラグの一部（H/I、J.txt、-c、user.name）は名前として数えない（数えなければ信用もしない）。
	got := trustedNames("S=/x; D=$S/y; cd ${S}; git -C $D -c user.name=x reset; for n in 1; do E=$n; done; F=1; F=2; G=${G:-1}; echo H/I J.txt")
	for name, want := range map[string]bool{
		"S": true, "D": true, "n": true, "E": true, "F": false, "G": false, "name": false, "user": false, "c": false,
		"H": false, "I": false, "J": false,
	} {
		if got[name] != want {
			t.Errorf("trusted[%q] = %v, want %v", name, got[name], want)
		}
	}
}

func TestExpandWord(t *testing.T) {
	t.Setenv("HOME", "/Users/alice")
	vars := map[string]string{"S": "/s", "SP": "/a b", "E": "", "G": "/x*"}
	for _, testCase := range []struct {
		word     string
		assigned bool
		want     string
		ok       bool
	}{
		{"$S/x", false, "/s/x", true},
		{"${S}x", false, "/sx", true},
		{`"$S"/'y z'`, false, "/s/y z", true},
		{`"${SP}"`, false, "/a b", true},
		{"$SP", false, "", false},
		{"$SP", true, "/a b", true},
		{"$G", false, "", false},
		{`"$G"`, false, "/x*", true},
		{"$E", false, "", false},
		{`"$E"`, false, "", false},
		{"$U", false, "", false},
		{"$1", false, "", false},
		{"${S:-x}", false, "", false},
		{"~/x", true, "/Users/alice/x", true},
		{"~", false, "/Users/alice", true},
		{"a~", false, "", false},
		{`"~/x"`, false, "~/x", true},
		{`"\$S"`, false, "", false},
		{"'x", false, "", false},
		{`"x`, false, "", false},
		{"x/*", false, "", false},
	} {
		got, ok := expandWord(testCase.word, vars, testCase.assigned)
		if got != testCase.want || ok != testCase.ok {
			t.Errorf("expandWord(%q, %v) = %q, %v; want %q, %v", testCase.word, testCase.assigned, got, ok, testCase.want, testCase.ok)
		}
	}
}

func TestErrexitFlag(t *testing.T) {
	for command, want := range map[string]string{
		"set -e": "on", "set -eu": "on", "set -euo pipefail": "on", "set -o errexit": "on", "set -uo pipefail -e": "on",
		"set +e": "off", "set +o errexit": "off", "set -u": "none", "set -o pipefail": "none", "set -- -e": "none", "set x -e": "none",
	} {
		on, ok := errexitFlag(py.Fields(command), 1)
		got := map[bool]string{true: "on", false: "off"}[on]
		if !ok {
			got = "none"
		}
		if got != want {
			t.Errorf("%q: %s, want %s", command, got, want)
		}
	}
}

func TestSeparatorClassifiers(t *testing.T) {
	for seps, want := range map[string]bool{"": true, "&&": true, "&&\n": true, "&&(": true, ")&&": true, ";": false, "\n": false, "||": false, "|": false, "&": false} {
		if got := onlyAndThen(seps); got != want {
			t.Errorf("onlyAndThen(%q) = %v", seps, got)
		}
	}
	for seps, want := range map[string]bool{"&&": false, "||": false, ";": false, "|": true, "&": true, "&\n": true, "&&|": true} {
		if got := hasLonePipeOrAmpersand(seps); got != want {
			t.Errorf("hasLonePipeOrAmpersand(%q) = %v", seps, got)
		}
	}
}

func TestStripFileHeredocs(t *testing.T) {
	for _, testCase := range []struct {
		command, want string
		bodies        []string
	}{
		{"cat > a.sh <<'EOF'\ngit reset --hard\nEOF\necho ok", "cat > a.sh <<'EOF'\nEOF\necho ok", nil},
		{"cat >a.sh <<-EOF\n\tgit reset --hard\n\tEOF\n", "cat >a.sh <<-EOF\n\tEOF\n", nil},
		{"cat <<\"EOF\" >> x/a.sh\ngit reset --hard\nEOF", "cat <<\"EOF\" >> x/a.sh\nEOF", nil},
		{"tee -a a.sh <<EOF\ngit reset --hard\nEOF", "tee -a a.sh <<EOF\nEOF", nil},
		// 閉じない本文は末尾まで。
		{"cat > a.sh <<'EOF'\ngit reset --hard", "cat > a.sh <<'EOF'\n", nil},
		// 実行されうる本文は残す。
		{"bash <<'EOF'\ngit reset --hard\nEOF", "", []string{"git reset --hard\n"}},
		{"cat <<'EOF' | sh\ngit reset --hard\nEOF", "", []string{"git reset --hard\n"}},
		{"cat <<'EOF'\ngit reset --hard\nEOF", "", []string{"git reset --hard\n"}},
		{"cat 2>&1 <<'EOF'\ngit reset --hard\nEOF", "", []string{"git reset --hard\n"}},
		{"x=$(cat > a <<'EOF'\ngit reset --hard\nEOF\n)", "", []string{"git reset --hard\n"}},
		{"cat > >(sh) <<'EOF'\ngit reset --hard\nEOF", "", []string{"git reset --hard\n"}},
		{"cat > a.sh <<EOF\n$(git reset --hard)\nEOF", "", []string{"$(git reset --hard)\n"}},
		{"cat > a.sh <<EOF\n`git reset --hard`\nEOF", "", []string{"`git reset --hard`\n"}},
		{"cat > a.sh <<'EOF'\ngit reset --hard\nEOF\nsh ./a.sh", "", []string{"git reset --hard\n"}},
		// 演算子ではない <<。
		{"echo '<<EOF'\ngit reset --hard", "", nil},
		{"cat <<< x\ngit reset --hard", "", nil},
		{"echo x # <<EOF\ngit reset --hard", "", nil},
		// 1 行に 2 つ。書き出す方だけを外す。
		{"cat > a.txt <<A; bash <<B\nx\nA\ny\nB\nz", "cat > a.txt <<A; bash <<B\nA\ny\nB\nz", []string{"y\n"}},
		// 書き出すファイルの名前が本文の外にもう一度現れれば（短い名前の偶然の一致を含む）残す。
		{"cat > a <<A; bash <<B\nx\nA\ny\nB\nz", "", []string{"x\n", "y\n"}},
	} {
		got, bodies := stripFileHeredocs(testCase.command)
		want := testCase.want
		if want == "" {
			want = testCase.command
		}
		if got != want {
			t.Errorf("%q: stripped %q, want %q", testCase.command, got, want)
		}
		var texts []string
		for _, body := range bodies {
			texts = append(texts, got[body.start:body.end])
		}
		if !reflect.DeepEqual(texts, testCase.bodies) {
			t.Errorf("%q: bodies %q, want %q", testCase.command, texts, testCase.bodies)
		}
	}
}
