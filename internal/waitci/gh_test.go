package waitci

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HappyOnigiri/hhx/internal/i18n"
)

// call は偽の Runner が 1 回に返す応答である。
type call struct {
	result Result
	err    error
}

// fakeRunner は呼び出しを記録し、responses を順に返す。最後の要素は以降ずっと返る。
type fakeRunner struct {
	responses []call
	calls     [][]string
	timeouts  []time.Duration
}

func (f *fakeRunner) Run(name string, args []string, timeout time.Duration) (Result, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	f.timeouts = append(f.timeouts, timeout)
	response := f.responses[min(len(f.calls)-1, len(f.responses)-1)]
	return response.result, response.err
}

func stdout(text string) call { return call{result: Result{Stdout: text}} }

func failure(stderr string) call { return call{result: Result{Stderr: stderr, Code: 1}} }

// --- PullRequestLookup ---

func TestReturnsThePRFoundForTheCommit(t *testing.T) {
	runner := &fakeRunner{responses: []call{stdout("34\n")}}
	number, err := GH{Language: testLanguage, Runner: runner}.FindPRBySHA(strings.Repeat("A", 40))
	if err != nil || number != "34" {
		t.Fatalf("number=%q err=%v", number, err)
	}
	want := []string{
		"gh", "pr", "list", "--search", strings.Repeat("A", 40),
		"--state", "open", "--json", "number", "--jq", ".[].number", "--limit", "1",
	}
	if !reflect.DeepEqual(runner.calls[0], want) || runner.timeouts[0] != GHTimeout {
		t.Fatalf("calls=%q timeouts=%v", runner.calls, runner.timeouts)
	}
}

func TestEmptySearchIsNoPullRequest(t *testing.T) {
	_, err := GH{Language: testLanguage, Runner: &fakeRunner{responses: []call{stdout(" \n")}}}.FindPRBySHA("abc")
	var noPR *NoPullRequestError
	if !errors.As(err, &noPR) || noPR.Message != messages.Text(testLanguage, idNoPR, map[string]any{"SHA": "abc"}) {
		t.Fatalf("err=%v", err)
	}
}

func TestLookupFailuresAreClassified(t *testing.T) {
	cases := []struct {
		response  call
		noPR      bool
		retryable bool
	}{
		{failure("no open pull requests match\n"), true, false},
		{failure("Not a git repository\n"), false, false},
		{failure("HTTP 502\n"), false, true},
		{call{err: errors.New("exec failed")}, false, true},
	}
	for _, tc := range cases {
		_, err := GH{Language: testLanguage, Runner: &fakeRunner{responses: []call{tc.response}}}.FindPRBySHA("abc")
		var noPR *NoPullRequestError
		var fetchErr *FetchError
		switch {
		case tc.noPR:
			if !errors.As(err, &noPR) {
				t.Errorf("%+v: err=%v", tc.response, err)
			}
		case !errors.As(err, &fetchErr) || fetchErr.Retryable != tc.retryable:
			t.Errorf("%+v: err=%#v", tc.response, err)
		}
	}
}

func TestMissingSHAIsNotLookedUp(t *testing.T) {
	runner := &fakeRunner{responses: []call{stdout("1")}}
	_, err := GH{Language: testLanguage, Runner: runner}.FindPRBySHA("")
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) || fetchErr.Retryable || fetchErr.Message != messages.T(testLanguage, idNoDetachedHead) ||
		len(runner.calls) != 0 {
		t.Fatalf("err=%#v calls=%q", err, runner.calls)
	}
	// Error() はログとテストのため表示言語によらず英語で、表示には DisplayMessage が表示言語の文面を返す。
	if err.Error() != messages.T(i18n.English, idNoDetachedHead) || DisplayMessage(err) != fetchErr.Message {
		t.Fatalf("Error()=%q DisplayMessage()=%q", err.Error(), DisplayMessage(err))
	}
}

// --- Fetch ---

func TestMergeableIsRequestedInTheSameCall(t *testing.T) {
	runner := &fakeRunner{responses: []call{
		stdout(`{"headRefOid": "abc", "mergeable": "CONFLICTING", "statusCheckRollup": []}`),
	}}
	snapshot, err := GH{Language: testLanguage, Runner: runner}.Fetch("213")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gh", "pr", "view", "213", "--json", "headRefOid,mergeable,statusCheckRollup"}
	if !reflect.DeepEqual(runner.calls[0], want) || !snapshot.Conflicting() || snapshot.Head != "abc" {
		t.Fatalf("calls=%q snapshot=%+v", runner.calls, snapshot)
	}
}

func TestFetchWithoutAReferenceUsesTheCurrentBranch(t *testing.T) {
	runner := &fakeRunner{responses: []call{stdout(`{"statusCheckRollup": null}`)}}
	snapshot, err := GH{Language: testLanguage, Runner: runner}.Fetch("")
	if err != nil || snapshot.Head != "" || snapshot.Mergeable != "" || len(snapshot.Checks) != 0 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if want := []string{"gh", "pr", "view", "--json", "headRefOid,mergeable,statusCheckRollup"}; !reflect.DeepEqual(
		runner.calls[0], want) {
		t.Fatalf("calls=%q", runner.calls)
	}
}

func TestFetchNormalizesTheRollup(t *testing.T) {
	runner := &fakeRunner{responses: []call{stdout(`{"headRefOid":"abc","statusCheckRollup":[` +
		`{"__typename":"CheckRun","name":"build","workflowName":"CI","status":"COMPLETED","conclusion":"FAILURE"},` +
		`{"__typename":"StatusContext","context":"legacy","state":"SUCCESS"}]}`)}}
	snapshot, err := GH{Language: testLanguage, Runner: runner}.Fetch("1")
	if err != nil || !reflect.DeepEqual(names(snapshot.Checks), []string{"CI / build", "legacy"}) {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	// 配列でない rollup は 0 件として読む。
	runner = &fakeRunner{responses: []call{stdout(`{"headRefOid":"abc","statusCheckRollup":{"a":1}}`)}}
	if snapshot, err = (GH{Language: testLanguage, Runner: runner}).Fetch("1"); err != nil || len(snapshot.Checks) != 0 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

func TestFetchFailuresAreClassified(t *testing.T) {
	cases := []struct {
		name      string
		response  call
		noPR      bool
		retryable bool
		message   string
	}{
		{"no pr", failure("no pull requests found for branch \"x\"\n"), true, false, `no pull requests found for branch "x"`},
		{
			"fatal", failure("GraphQL: Could not resolve to a PullRequest with the number of 9.\n"), false, false,
			"GraphQL: Could not resolve to a PullRequest with the number of 9.",
		},
		{"transient", failure("HTTP 502: Bad Gateway\n"), false, true, "HTTP 502: Bad Gateway"},
		{"not json", stdout("{ this is not json"), false, true, ""},
		{"not an object", stdout("[]"), false, true, ""},
		{
			"spawn",
			call{err: errors.New("[Errno 2] No such file or directory: 'gh'")},
			false, true,
			"[Errno 2] No such file or directory: 'gh'",
		},
	}
	for _, tc := range cases {
		_, err := GH{Language: testLanguage, Runner: &fakeRunner{responses: []call{tc.response}}}.Fetch("1")
		var noPR *NoPullRequestError
		var fetchErr *FetchError
		switch {
		case tc.noPR:
			if !errors.As(err, &noPR) || noPR.Message != tc.message {
				t.Errorf("%s: err=%#v", tc.name, err)
			}
		case !errors.As(err, &fetchErr) || fetchErr.Retryable != tc.retryable:
			t.Errorf("%s: err=%#v", tc.name, err)
		case tc.message != "" && fetchErr.Message != tc.message:
			t.Errorf("%s: message=%q", tc.name, fetchErr.Message)
		case tc.message == "" && !strings.HasPrefix(fetchErr.Message, messages.Text(testLanguage, idJSONUnread, map[string]any{"Error": ""})):
			t.Errorf("%s: message=%q", tc.name, fetchErr.Message)
		}
	}
}

// --- CiEvidence ---

func TestRegisteredWorkflowIsEnough(t *testing.T) {
	runner := &fakeRunner{responses: []call{stdout("2\n")}}
	found, err := GH{Language: testLanguage, Runner: runner}.CIEvidence()
	if err != nil || !found || len(runner.calls) != 1 {
		t.Fatalf("found=%v err=%v calls=%q", found, err, runner.calls)
	}
	want := []string{"gh", "api", "repos/{owner}/{repo}/actions/workflows", "--jq", workflowCountQuery}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("calls=%q", runner.calls)
	}
}

func TestWorkflowCountQueryIgnoresDynamicWorkflows(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq が無い")
	}
	for _, tc := range []struct {
		name, response, want string
	}{
		{"none", `{"total_count":0,"workflows":[]}`, "0"},
		{"dynamic only", `{"total_count":1,"workflows":[{"path":"dynamic/copilot-swe-agent/copilot"}]}`, "0"},
		{"mixed", `{"total_count":2,"workflows":[{"path":"dynamic/pages/pages-build-deployment"},{"path":".github/workflows/ci.yml"}]}`, "1"},
		// ページの外の workflow は中身が分からないので形跡として数える。
		{"beyond the page", `{"total_count":40,"workflows":[{"path":"dynamic/copilot-swe-agent/copilot"}]}`, "39"},
	} {
		command := exec.CommandContext(t.Context(), jq, workflowCountQuery)
		command.Stdin = strings.NewReader(tc.response)
		output, err := command.Output()
		if err != nil || strings.TrimSpace(string(output)) != tc.want {
			t.Errorf("%s: output=%q err=%v", tc.name, output, err)
		}
	}
}

func TestExternalStatusWithoutAWorkflowCountsAsCI(t *testing.T) {
	// Vercel・Codecov のように .github/workflows に現れない CI を落とさない。
	runner := &fakeRunner{responses: []call{stdout("0"), stdout("3")}}
	found, err := GH{Language: testLanguage, Runner: runner}.CIEvidence()
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	want := []string{
		"gh", "pr", "list", "--state", "merged", "--limit", "5", "--json", "statusCheckRollup",
		"--jq", "[.[].statusCheckRollup | length] | add // 0",
	}
	if !reflect.DeepEqual(runner.calls[1], want) {
		t.Fatalf("calls=%q", runner.calls)
	}
}

func TestHugeCountIsStillACount(t *testing.T) {
	found, err := GH{Language: testLanguage, Runner: &fakeRunner{responses: []call{stdout("99999999999999999999999")}}}.CIEvidence()
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestNoWorkflowAndNoPastCheckIsNoCI(t *testing.T) {
	found, err := GH{Language: testLanguage, Runner: &fakeRunner{responses: []call{stdout("0"), stdout("0")}}}.CIEvidence()
	if err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestUnreadableOutputIsUndecidable(t *testing.T) {
	for _, output := range []string{"null", "", "-1", "1.5", "+1", "٣"} {
		_, err := GH{Language: testLanguage, Runner: &fakeRunner{responses: []call{stdout(output)}}}.CIEvidence()
		var fetchErr *FetchError
		if !errors.As(err, &fetchErr) {
			t.Errorf("%q: err=%v", output, err)
		}
	}
	_, err := GH{Language: testLanguage, Runner: &fakeRunner{responses: []call{stdout("0"), stdout("null")}}}.CIEvidence()
	if err == nil || DisplayMessage(err) != messages.Text(testLanguage, idCountUnread, map[string]any{"Output": "'null'"}) ||
		err.Error() != messages.Text(i18n.English, idCountUnread, map[string]any{"Output": "'null'"}) {
		t.Fatalf("err=%v", err)
	}
}

func TestFailedLookupIsUndecidable(t *testing.T) {
	for _, responses := range [][]call{
		{failure("gh が落ちた")},
		{stdout("0"), failure("gh が落ちた")},
		{call{err: errors.New("boom")}},
	} {
		_, err := GH{Language: testLanguage, Runner: &fakeRunner{responses: responses}}.CIEvidence()
		var fetchErr *FetchError
		if !errors.As(err, &fetchErr) {
			t.Errorf("%+v: err=%v", responses, err)
		}
	}
}

// --- git ---

func TestGitFailuresReadAsEmpty(t *testing.T) {
	runner := &fakeRunner{responses: []call{stdout(" abc \n"), failure("fatal"), {err: errors.New("boom")}}}
	git := Git{Runner: runner}
	if got := git.HeadSHA(); got != "abc" {
		t.Fatalf("head=%q", got)
	}
	if git.Output("x") != "" || git.Output("y") != "" {
		t.Fatal("a failed git must read as empty")
	}
	if !reflect.DeepEqual(runner.calls[0], []string{"git", "rev-parse", "HEAD"}) || runner.timeouts[0] != gitTimeout {
		t.Fatalf("calls=%q timeouts=%v", runner.calls, runner.timeouts)
	}
}

// --- ExecRunner ---

func TestExecRunnerCollectsOutputAndExitCode(t *testing.T) {
	result, err := ExecRunner{}.Run("sh", []string{"-c", "printf out; printf err >&2; exit 3"}, 10*time.Second)
	if err != nil || result != (Result{Stdout: "out", Stderr: "err", Code: 3}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestExecRunnerStopsAtTheTimeout(t *testing.T) {
	started := time.Now()
	_, err := ExecRunner{}.Run("sleep", []string{"5"}, 100*time.Millisecond)
	if err == nil || err.Error() != "Command '['sleep', '5']' timed out after 0.1 seconds" {
		t.Fatalf("err=%v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("the timeout did not stop the command")
	}
}

func TestExecRunnerReportsAMissingCommand(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-gh")
	_, err := ExecRunner{}.Run(missing, nil, time.Second)
	if err == nil || err.Error() != "[Errno 2] No such file or directory: '"+missing+"'" {
		t.Fatalf("err=%v", err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "x"), []byte("not executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (ExecRunner{}).Run(filepath.Join(directory, "x"), nil, time.Second); err == nil {
		t.Fatal("a non-executable file must fail to start")
	}
}

func TestGHTimeoutIsConfigurable(t *testing.T) {
	runner := &fakeRunner{responses: []call{stdout("1")}}
	if _, err := (GH{Language: testLanguage, Runner: runner, Timeout: time.Second}).FindPRBySHA("abc"); err != nil || runner.timeouts[0] != time.Second {
		t.Fatalf("err=%v timeouts=%v", err, runner.timeouts)
	}
}

func TestPythonRepr(t *testing.T) {
	for value, want := range map[string]string{
		"gh":      "'gh'",
		"it's":    `"it's"`,
		`a'b"c`:   `'a\'b"c'`,
		"a\\b\nc": `'a\\b\nc'`,
	} {
		if got := pythonRepr(value); got != want {
			t.Errorf("%q: got %s, want %s", value, got, want)
		}
	}
}
