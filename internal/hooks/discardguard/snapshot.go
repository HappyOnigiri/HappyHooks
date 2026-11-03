package discardguard

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

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
	// failure は最後に失敗した git の様子である。deny の原因を記録に残すために持つ（nil なら残さない）。
	failure *gitFailure
}

// gitFailure は失敗した git の起動の様子である。snapshot を作れずに deny した原因を後から調べるために記録する。
type gitFailure struct {
	Args     []string `json:"git_args,omitempty"`
	ExitCode int      `json:"exit_code"`
	// TimedOut は git 1 回の上限か hook 全体の持ち時間を使い切ったことを示す。
	TimedOut  bool   `json:"timed_out"`
	Stderr    string `json:"stderr,omitempty"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

// notARepository は、git がディレクトリをリポジトリでないと答えて失敗したかを返す。
// git の表示言語に左右されないよう、git の起動では LC_ALL=C にしている。
func (f *gitFailure) notARepository() bool {
	return f != nil && !f.TimedOut && f.ExitCode == 128 && strings.Contains(f.Stderr, "not a git repository")
}

// 記録の stderr とコマンドの長さの上限（文字数）。
const (
	maxRecordedStderr  = 1000
	maxRecordedCommand = 4000
)

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
		r.fail(args, -1, true, "", 0)
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), min(gitTimeout, remaining))
	defer cancel()
	// hookexec.Output は Env・stdin・stderr・停止のシグナルを渡せないので、ここで直接起動する。
	//nolint:forbidigo // 一時 index の lock を消せるよう SIGTERM で止める必要があり、hookexec では代えられない
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, args...)...)
	// 失敗の判別（not a git repository）を stderr の文面で行うので、git のメッセージを英語に固定する。
	command.Env = append(append(os.Environ(), "LC_ALL=C"), env...)
	// タイムアウトでは SIGKILL ではなく SIGTERM で止める。git は SIGTERM を捕捉して一時 index の lock を消すが、
	// SIGKILL だと lock が残り、以後その作業ツリーの snapshot が失敗し続ける（破棄系のコマンドがすべて deny になる）。
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	// SIGTERM で終わらない場合と、子孫のプロセスが出力を開いたまま残った場合に、この猶予の後に kill して待ち続けない。
	command.WaitDelay = time.Second
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	start := time.Now()
	if err := command.Run(); err != nil {
		exitCode := -1
		if exitError := (*exec.ExitError)(nil); errors.As(err, &exitError) {
			exitCode = exitError.ExitCode()
		}
		r.fail(args, exitCode, ctx.Err() != nil, stderr.String(), time.Since(start).Milliseconds())
		return "", false
	}
	return stdout.String(), true
}

// fail は失敗した git の様子を failure に残す。
func (r gitRunner) fail(args []string, exitCode int, timedOut bool, stderr string, elapsedMS int64) {
	if r.failure == nil {
		return
	}
	*r.failure = gitFailure{
		Args:      slices.Clone(args),
		ExitCode:  exitCode,
		TimedOut:  timedOut,
		Stderr:    clipRunes(py.Strip(strings.ToValidUTF8(stderr, "�")), maxRecordedStderr),
		ElapsedMS: elapsedMS,
	}
}

// clipRunes は text を limit 文字（コードポイント）までに切る。
func clipRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit]) + " …"
}
