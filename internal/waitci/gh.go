package waitci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/HappyOnigiri/hhx/internal/i18n"
	"github.com/HappyOnigiri/hhx/internal/pycompat"
)

const (
	// GHTimeout は gh 1 回の呼び出しの上限である。
	GHTimeout = 30 * time.Second
	// gitTimeout は git 1 回の呼び出しの上限である。
	gitTimeout = 10 * time.Second
	// waitDelay は時間切れで止めたプロセスの出力を待つ上限である。子プロセスがパイプを握ったままでも戻る。
	waitDelay = time.Second
)

// Result はコマンドの実行結果である。
type Result struct {
	Stdout string
	Stderr string
	Code   int
}

// Runner は外部コマンドを実行する。起動の失敗と時間切れは error で返し、0 以外の終了は Result.Code で返す。
type Runner interface {
	Run(name string, args []string, timeout time.Duration) (Result, error)
}

// ExecRunner は PATH から外部コマンドを起動する Runner である。
type ExecRunner struct{}

// Run はコマンドを起動し、stdout と stderr を集める。stdin は渡さない。
func (ExecRunner) Run(name string, args []string, timeout time.Duration) (Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// wait-ci は hook ではなく、stderr・終了コード・Python 互換のエラー文が出力の契約なので、hookexec を通さない。
	//nolint:forbidigo // wait-ci の Runner の実体で、時間の上限は呼び出し側の timeout で付ける
	command := exec.CommandContext(ctx, name, args...)
	command.WaitDelay = waitDelay
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if ctx.Err() != nil {
		// 文言は Python の subprocess.TimeoutExpired に合わせる。gh の失敗として結論行に出ることがある。
		return Result{}, fmt.Errorf("Command '%s' timed out after %s seconds", //nolint:staticcheck // 文言の互換のため大文字で始める
			pythonList(append([]string{name}, args...)),
			strconv.FormatFloat(timeout.Seconds(), 'f', -1, 64))
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, syscall.ENOENT) {
			// 文言は Python の FileNotFoundError に合わせる。
			return Result{}, fmt.Errorf("[Errno 2] No such file or directory: %s", pythonRepr(name))
		}
		return Result{}, err
	}
	return Result{Stdout: stdout.String(), Stderr: stderr.String(), Code: command.ProcessState.ExitCode()}, nil
}

// pythonRepr は Python の repr(str) を ASCII の範囲で再現する。
func pythonRepr(value string) string {
	quote := "'"
	if strings.Contains(value, "'") && !strings.Contains(value, `"`) {
		quote = `"`
	}
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	if quote == "'" {
		escaped = strings.ReplaceAll(escaped, "'", `\'`)
	}
	escaped = strings.NewReplacer("\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(escaped)
	return quote + escaped + quote
}

func pythonList(values []string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = pythonRepr(value)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// GH は gh を外部コマンドとして呼ぶ。認証と {owner}/{repo} の解決は gh に任せる。
type GH struct {
	Runner Runner
	// Language はエラーの文面の表示言語である。零値は英語になる。
	Language i18n.Language
	// Timeout は 1 回の呼び出しの上限である。0 なら GHTimeout。
	Timeout time.Duration
}

func (g GH) run(args []string) (Result, error) {
	timeout := g.Timeout
	if timeout == 0 {
		timeout = GHTimeout
	}
	result, err := g.Runner.Run("gh", args, timeout)
	if err != nil {
		return Result{}, &FetchError{Message: err.Error(), Retryable: true}
	}
	return result, nil
}

// classify は 0 以外で終わった gh の stderr を、PR が無い・再試行できる失敗・できない失敗に分ける。
func classify(stderr string) error {
	message := pycompat.Strip(stderr)
	lowered := pycompat.Lower(stderr)
	if containsAny(lowered, NoPRMarkers) {
		return &NoPullRequestError{Message: message}
	}
	return &FetchError{Message: message, Retryable: !containsAny(lowered, fatalMarkers)}
}

func containsAny(text string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// capture は gh を 1 回呼んで stdout を返す。失敗は *FetchError にする。
func (g GH) capture(args []string) (string, error) {
	result, err := g.run(args)
	if err != nil {
		return "", err
	}
	if result.Code != 0 {
		return "", &FetchError{Message: pycompat.Strip(result.Stderr), Retryable: true}
	}
	return pycompat.Strip(result.Stdout), nil
}

func asCount(language i18n.Language, text string) (int, error) {
	// Python の str.isdigit と int。gh の --jq の出力は ASCII の数字なので ASCII に限る。
	if text == "" || strings.Trim(text, "0123456789") != "" {
		data := map[string]any{"Output": pythonRepr(text)}
		return 0, &FetchError{
			Message: messages.Text(language, idCountUnread, data), English: messages.Text(i18n.English, idCountUnread, data),
			Retryable: true,
		}
	}
	count, err := strconv.Atoi(text)
	if err != nil {
		// 桁あふれ。Python の int は任意精度なので、件数として読める大きな数である。
		return math.MaxInt, nil //nolint:nilerr // 数字だけの文字列が Atoi で失敗するのは桁あふれに限る
	}
	return count, nil
}

// workflowCountQuery は dynamic/ の workflow を除いた workflow の数を返す jq の式である。
const workflowCountQuery = `.total_count - ([.workflows[] | select(.path | startswith("dynamic/"))] | length)`

// CIEvidence はこの repo で check が動く形跡があれば真を返す。判定できなければ *FetchError を返す。
//
// 根拠は 2 つ取る。Actions の workflow が 1 つでも登録されているか、直近の merged PR に
// check が付いていたか。後者を見るのは、`.github/workflows` に現れない外部の status
// (Vercel・Codecov 等) しか持たない repo を「CI 無し」と誤判定しないためである。
// どちらかの問い合わせが失敗したら、片方だけで「無い」と決めずに判定不能として返す。
// ここで誤って「無い」と答えると、実在する CI の失敗を見落とすことになる。
//
// workflow の数からは、GitHub が自動で登録する path が `dynamic/` の workflow
// (Copilot cloud agent 等) を除く。PR の check を作らないのに、CI の無い repo にも現れるためである。
// 除くのは取得した 1 ページ目に見えたものだけにし、ページの外の workflow は形跡として数える。
func (g GH) CIEvidence() (bool, error) {
	output, err := g.capture([]string{"api", "repos/{owner}/{repo}/actions/workflows", "--jq", workflowCountQuery})
	if err != nil {
		return false, err
	}
	workflows, err := asCount(g.Language, output)
	if err != nil {
		return false, err
	}
	if workflows > 0 {
		return true, nil
	}
	output, err = g.capture([]string{
		"pr", "list", "--state", "merged", "--limit", strconv.Itoa(ciSample), "--json", "statusCheckRollup",
		// null (rollup を持たない PR) は length が 0 になる。空配列は add が null。
		"--jq", "[.[].statusCheckRollup | length] | add // 0",
	})
	if err != nil {
		return false, err
	}
	checks, err := asCount(g.Language, output)
	if err != nil {
		return false, err
	}
	return checks > 0, nil
}

// Fetch は PR の head commit・マージ可否・check 一覧を 1 回の gh 呼び出しで取る。reference が空なら現在の branch。
func (g GH) Fetch(reference string) (Snapshot, error) {
	args := []string{"pr", "view"}
	if reference != "" {
		args = append(args, reference)
	}
	args = append(args, "--json", "headRefOid,mergeable,statusCheckRollup")
	result, err := g.run(args)
	if err != nil {
		return Snapshot{}, err
	}
	if result.Code != 0 {
		return Snapshot{}, classify(result.Stderr)
	}
	var payload any
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		data := map[string]any{"Error": err.Error()}
		return Snapshot{}, &FetchError{
			Message: messages.Text(g.Language, idJSONUnread, data), English: messages.Text(i18n.English, idJSONUnread, data),
			Retryable: true,
		}
	}
	object, ok := payload.(map[string]any)
	if !ok {
		// Python 実装は AttributeError で落ちる。読めない出力として再試行する。
		return Snapshot{}, &FetchError{
			Message: messages.T(g.Language, idJSONNotObject), English: messages.T(i18n.English, idJSONNotObject),
			Retryable: true,
		}
	}
	// 配列でない rollup は、Python では要素がオブジェクトでないので読み飛ばされ、0 件になる。
	rollup, _ := object["statusCheckRollup"].([]any)
	return Snapshot{
		Head:      stringField(object, "headRefOid"),
		Mergeable: stringField(object, "mergeable"),
		Checks:    Normalize(rollup),
	}, nil
}

// FindPRBySHA は commit を含む open PR の番号を返す。
func (g GH) FindPRBySHA(sha string) (string, error) {
	if sha == "" {
		return "", &FetchError{
			Message: messages.T(g.Language, idNoDetachedHead), English: messages.T(i18n.English, idNoDetachedHead),
			Retryable: false,
		}
	}
	result, err := g.run([]string{
		"pr", "list", "--search", sha, "--state", "open", "--json", "number", "--jq", ".[].number", "--limit", "1",
	})
	if err != nil {
		return "", err
	}
	if result.Code != 0 {
		return "", classify(result.Stderr)
	}
	number := pycompat.Strip(result.Stdout)
	if number == "" {
		data := map[string]any{"SHA": sha}
		return "", &NoPullRequestError{
			Message: messages.Text(g.Language, idNoPR, data), English: messages.Text(i18n.English, idNoPR, data),
		}
	}
	return number, nil
}

// Git は git を呼び、失敗は空の出力として扱う。
type Git struct {
	Runner Runner
}

// Output は git を 1 回呼んで、成功したときの stdout を返す。失敗・時間切れは空を返す。
func (g Git) Output(args ...string) string {
	result, err := g.Runner.Run("git", args, gitTimeout)
	if err != nil || result.Code != 0 {
		return ""
	}
	return pycompat.Strip(result.Stdout)
}

// HeadSHA は現在の HEAD の commit を返す。取れなければ空を返す。
func (g Git) HeadSHA() string { return g.Output("rev-parse", "HEAD") }

// IsDetached は参照先の指定がなく、現在のリポジトリが detached HEAD なら真を返す。
func IsDetached(reference string, git func(args ...string) string) bool {
	if reference != "" {
		return false
	}
	if git("rev-parse", "--git-dir") == "" {
		return false
	}
	return git("symbolic-ref", "-q", "--short", "HEAD") == ""
}
