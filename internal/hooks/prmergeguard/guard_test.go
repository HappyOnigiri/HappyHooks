package prmergeguard

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HappyOnigiri/hhx/internal/hooktest"
)

// 理由文に出るラベル（どのルールが発火したかの判別用）。テストを流す言語でカタログから引く。
var (
	labelPRMerge = messages.T(hooktest.Language, idTargetPRMerge)
	labelAPI     = messages.T(hooktest.Language, idTargetAPI)
	labelGraphQL = messages.T(hooktest.Language, idTargetGraphQL)
	labelHTTP    = messages.T(hooktest.Language, idTargetHTTP)
)

// 実行者のシェルで開錠されたままだと拒否のテストが全滅して気付きにくいので、先に外す。
func TestMain(m *testing.M) {
	_ = os.Unsetenv(UnlockEnv)
	os.Exit(m.Run())
}

func check(t *testing.T, want string, cases []hooktest.Case) {
	t.Helper()
	hooktest.CheckTable(t, Definition(), want, "/tmp", cases)
}

func TestBlocksGHPRMerge(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "gh pr merge 123", Label: labelPRMerge},
		{Command: "gh pr merge", Label: labelPRMerge},
		{Command: "gh pr merge --squash --delete-branch 42", Label: labelPRMerge},
		{Command: "gh pr merge --admin --merge 42", Label: labelPRMerge},
		{Command: "rtk gh pr merge 5 --squash", Label: labelPRMerge},
		{Command: "GH_TOKEN=xxx gh pr merge 1", Label: labelPRMerge},
		{Command: "gh  pr\tmerge  7", Label: labelPRMerge},
		{Command: "cd /tmp && gh pr merge 1", Label: labelPRMerge},
		{Command: "gh pr merge 1 2>/dev/null", Label: labelPRMerge},
		{Command: "echo start\ngh pr merge 1", Label: labelPRMerge},
		{Command: "gh pr merge 1\n", Label: labelPRMerge},
		{Command: "gh pr merge\n", Label: labelPRMerge},
		// Python の \s と同じく、Unicode の空白も区切りとして扱う。
		{Command: "gh　pr merge 1", Label: labelPRMerge},
		{Command: "gh\x1cpr merge 1", Label: labelPRMerge},
	})
}

// 区切り文字と語がくっついた形。空白の境界だけだと取り逃す。
func TestBlocksShellSeparatorBoundary(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "git status; gh pr merge 1", Label: labelPRMerge},
		{Command: "true;gh pr merge 1", Label: labelPRMerge},
		{Command: "(gh pr merge 1)", Label: labelPRMerge},
		{Command: "false||gh pr merge 1", Label: labelPRMerge},
		{Command: "git status&&gh pr merge 1", Label: labelPRMerge},
	})
}

func TestBlocksPathPrefixedInvocation(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "/usr/local/bin/gh pr merge 1", Label: labelPRMerge},
		{Command: "/opt/homebrew/bin/gh api -X PUT repos/o/r/pulls/1/merge", Label: labelAPI},
		{Command: "./gh pr merge 1", Label: labelPRMerge},
	})
}

// メソッドの表記ではなくエンドポイントで判定する。
func TestBlocksGHAPIMergeEndpoint(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "gh api -X PUT repos/o/r/pulls/1/merge", Label: labelAPI},
		{Command: "gh api --method=PUT repos/o/r/pulls/1/merge", Label: labelAPI},
		{Command: "gh api --method PUT repos/o/r/pulls/1/merge", Label: labelAPI},
		{Command: "gh api -XPUT repos/o/r/pulls/1/merge", Label: labelAPI},
		{Command: "gh api -X PUT /repos/o/r/pulls/1/merge", Label: labelAPI},
		{Command: `gh api "repos/o/r/pulls/1/merge" -f merge_method=squash`, Label: labelAPI},
		{Command: "gh api repos/o/r/pulls/1/merge?x=1", Label: labelAPI},
		{Command: "gh api repos/o/r/pulls/12345/merge", Label: labelAPI},
		{Command: "gh api repos/o/r/merges -f base=main -f head=topic", Label: labelAPI},
		{Command: "gh api -X POST repos/o/r/merges", Label: labelAPI},
		{Command: "gh api repos/my-org/my.repo/merges", Label: labelAPI},
		{Command: "gh api /repos/o/r/merges", Label: labelAPI},
		{Command: "gh api repos/o/r/pulls/1/merge\n", Label: labelAPI},
		// Python の \d は Unicode の数字にも一致する。
		{Command: "gh api repos/o/r/pulls/١/merge", Label: labelAPI},
	})
}

// 文字列が現れれば内容を問わず拒否する（意図的な過検出）。
func TestBlocksGraphQLMutation(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "gh api graphql -f query='mutation { mergePullRequest(input: {}) { x } }'", Label: labelGraphQL},
		{Command: "gh api graphql -f query='mutation { enablePullRequestAutoMerge(input: {}) { x } }'", Label: labelGraphQL},
		{Command: "gh api graphql -f query='MERGEPULLREQUEST'", Label: labelGraphQL},
		{Command: "gh api graphql -f query='EnablePullRequestAutoMerge'", Label: labelGraphQL},
		{Command: "curl -d 'mutation { mergePullRequest }' https://api.github.com/graphql", Label: labelGraphQL},
	})
}

func TestBlocksHTTPClient(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "curl -X PUT https://api.github.com/repos/o/r/pulls/1/merge", Label: labelHTTP},
		{Command: "curl -XPUT https://api.github.com/repos/o/r/pulls/1/merge", Label: labelHTTP},
		{Command: "curl -X PATCH https://api.github.com/repos/o/r/pulls/1", Label: labelHTTP},
		{Command: "curl -X POST https://api.github.com/repos/o/r/merges", Label: labelHTTP},
		{Command: "curl -s https://api.github.com/repos/o/r/pulls/1/merge", Label: labelHTTP},
		{Command: "wget --method=PUT https://api.github.com/repos/o/r/pulls/1/merge", Label: labelHTTP},
		{Command: "curl --method PUT https://api.github.com/x", Label: labelHTTP},
	})
}

// 誤爆はこの hook の実害なので、通す側を厚めに置く。
func TestAllowsUnrelatedCommands(t *testing.T) {
	check(t, "", hooktest.Commands(
		"ls -la",
		"npm run build",
		"echo legit",
		"digit=1",
		"echo 'merge the branch'",
		"make merge",
		"python3 -c 'print(1)'",
	))
}

func TestAllowsReadOnlyGH(t *testing.T) {
	check(t, "", hooktest.Commands(
		"gh pr view 123",
		"gh pr list --state merged",
		"gh pr diff 123",
		"gh pr checkout 123",
		"gh pr comment 1 --body 'merge later'",
		"gh pr edit 1 --add-label merge",
		"gh pr status",
		"gh auth status",
		"gh workflow run merge.yml",
		"gh release create v1 --notes merged",
		"gh pr list | grep merge",
	))
}

// merge が語の一部にすぎない形。
func TestAllowsGHPRMergeLookalikes(t *testing.T) {
	check(t, "", hooktest.Commands(
		"gh pr mergequeue foo",
		"gh pr merge-queue status",
		"foogh pr merge 1",
	))
}

func TestAllowsGHAPINonMergeEndpoints(t *testing.T) {
	check(t, "", hooktest.Commands(
		"gh api repos/o/r/pulls/1",
		"gh api -X PATCH repos/o/r/pulls/comments/12345 -f body=x",
		"gh api -X PATCH repos/o/r/issues/1 -f state=closed",
		"gh api -X PUT repos/o/r/issues/1/labels",
		"gh api repos/o/r/branches/feature/merge-fix/protection",
		"gh api repos/o/r/pulls/1/merge-info",
		"gh api repos/o/r/mergesomething",
		"gh api repos/o/r/merges-report",
		"gh api repos/o/r/pulls/1/mergeable",
		"gh api repos/o/r/commits/abc/pulls",
	))
}

// 利用者の判断で日常的に使うものとローカルの操作は塞がない。
func TestAllowsGitOperationsByPolicy(t *testing.T) {
	check(t, "", hooktest.Commands(
		"git status",
		"git log --oneline -5",
		"git push --force-with-lease origin HEAD",
		"git push origin :old-branch",
		"git push --force origin feature",
		"git branch -D old",
		"git merge --ff-only origin/main",
		"git merge origin/main",
		"git rebase origin/main",
		"git fetch origin main",
		"git fetch --all --prune",
		"git fetch origin pull-request-123",
		"git fetch origin refs/heads/pull",
		// PR の head の取得は調査にも使い、マージそのものではないので止めない。
		"git fetch origin pull/123/head",
		"git fetch origin pull/123/head:pr-123",
		"rtk git fetch origin pull/1/head",
		"/usr/bin/git fetch origin pull/1/head",
		"git fetch origin +refs/pull/1/head:refs/remotes/pr/1",
		"git fetch upstream refs/pull/9/head",
		"git fetch origin +pull/1/head:pr",
	))
}

func TestAllowsHTTPClientWithoutMerge(t *testing.T) {
	check(t, "", hooktest.Commands(
		"curl -s https://api.github.com/repos/o/r/pulls/1",
		"curl -s https://api.github.com/repos/o/r/issues",
		"curl -s https://example.com/api",
		"curl -X POST https://example.com/webhook",
		"curl -X PUT https://example.com/upload",
		"wget https://example.com/file.tar.gz",
	))
}

// 一次ゲートは git / gh / curl / wget を含まないコマンドを見ない。GraphQL の mutation 名だけを含む grep などは判定に入る前に抜ける。
func TestPrimaryGateLimitsScope(t *testing.T) {
	check(t, "", hooktest.Commands(
		"grep -rn mergePullRequest .",
		"rg enablePullRequestAutoMerge",
		"echo mergePullRequest",
	))
}

// 明示的な回避は対象外（README の既知の限界）。クォートの中は境界に含めない。
func TestDocumentedOutOfScope(t *testing.T) {
	check(t, "", hooktest.Commands(
		"echo 'gh pr merge'",
		"echo 'gh pr merge' > note.txt",
		`gh pr create --body "gh pr merge するときは..."`,
		"sh -c 'gh pr merge 1'",
		"bash -lc 'gh pr merge 1'",
	))
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"gh api --method=PUT x":   "gh api -X PUT x",
		"gh api --method PUT x":   "gh api -X PUT x",
		"gh api --method   PUT x": "gh api -X PUT x",
		"gh api --method=PATCH x": "gh api -X PATCH x",
		"curl -XPUT x":            "curl -X PUT x",
		"curl -XPOST -XPUT x":     "curl -X POST -X PUT x",
		"curl -X PUT x":           "curl -X PUT x",
		"curl -Xfoo x":            "curl -X foo x",
		"a\nb":                    "a b",
		// 似たフラグは書き換えない。
		"gh api --methodology=PUT x": "gh api --methodology=PUT x",
		"cmd --methods PUT":          "cmd --methods PUT",
		"cmd -x PUT":                 "cmd -x PUT",
	}
	for source, want := range cases {
		if got := normalize(source); got != want {
			t.Errorf("normalize(%q)=%q, want %q", source, got, want)
		}
	}
	for _, source := range []string{"gh api --method=PUT x", "curl -XPUT x", "gh pr merge 1"} {
		once := normalize(source)
		if twice := normalize(once); twice != once {
			t.Errorf("normalize is not idempotent for %q: %q then %q", source, once, twice)
		}
	}
}

func TestCommandFrom(t *testing.T) {
	cases := map[string]string{
		`{"tool_input": {"command": "gh pr merge 1"}}`: "gh pr merge 1",
		`{"tool_input": {}}`:                           "",
		`{"tool_input": null}`:                         "",
		`{}`:                                           "",
		`{"tool_input": {"command": 123}}`:             "",
		`{"tool_input": "gh pr merge"}`:                "",
		// JSON として読めない入力と object でない JSON は、生のまま判定に回す（fail-open にしない）。
		"gh pr merge 1": "gh pr merge 1",
		"[1, 2]":        "[1, 2]",
	}
	for raw, want := range cases {
		if got := commandFrom([]byte(raw)); got != want {
			t.Errorf("commandFrom(%q)=%q, want %q", raw, got, want)
		}
	}
}

func TestBrokenJSONIsJudgedAsCommand(t *testing.T) {
	if got := hooktest.Stdin(t, Definition(), "gh pr merge 1"); got.Decision != hooktest.Deny {
		t.Fatalf("raw command on stdin must be judged, got %+v", got)
	}
}

func TestOutputSchema(t *testing.T) {
	got := hooktest.Stdin(t, Definition(), `{"tool_input": {"command": "gh pr merge 1"}, "cwd": "/tmp"}`)
	if got.Decision != hooktest.Deny {
		t.Fatalf("decision=%q", got.Decision)
	}
	if want := reason(hooktest.Language, idTargetPRMerge); got.Reason != want {
		t.Errorf("reason=%q, want %q", got.Reason, want)
	}
}

func TestArgvDebugPath(t *testing.T) {
	if got := hooktest.Argv(t, Definition(), "gh pr merge 1"); got.Decision != hooktest.Deny {
		t.Errorf("argv deny: %+v", got)
	}
	if got := hooktest.Argv(t, Definition(), "git status"); got.Decision != "" {
		t.Errorf("argv allow: %+v", got)
	}
	// argv の文字列は payload として読まず、そのままコマンドとして判定する（"gh の引用符は境界にならない）。
	raw := `{"tool_input": {"command": "gh pr merge 1"}}`
	if got := hooktest.Argv(t, Definition(), raw); got.Decision != "" {
		t.Errorf("argv must be judged as a command string: %+v", got)
	}
}

func TestOddInputsDoNotCrash(t *testing.T) {
	for _, raw := range []string{
		"", "   ", "not json at all", "[]", "null", "0",
		`{"tool_input": {"command": null}}`,
		`{"tool_input": {"command": 123}}`,
		`{"tool_input": "gh pr merge"}`,
		`{"cwd": "/tmp"}`,
		`{"tool_input": {"command": ["gh pr merge 1"]}}`,
	} {
		if got := hooktest.Stdin(t, Definition(), raw); got.Decision != "" {
			t.Errorf("odd input %q: %+v", raw, got)
		}
	}
}

func TestLongCommandIsHandled(t *testing.T) {
	padding := strings.Repeat("x", 20000)
	check(t, "", hooktest.Commands("echo "+padding))
	check(t, hooktest.Deny, hooktest.Commands("echo "+padding+" && gh pr merge 1"))
}

func TestPathologicalInputIsFast(t *testing.T) {
	cases := []string{
		"gh api " + strings.Repeat("x", 20000) + " repos/o/r/pulls/1/mergeX",
		"curl " + strings.Repeat("-XPUT ", 2000) + "https://example.com/x",
		"gh api " + strings.Repeat("--method=PUT ", 2000) + "repos/o/r/pulls/1/x",
		"gh" + strings.Repeat(" ", 5000) + "pr view 1",
	}
	start := time.Now()
	for _, command := range cases {
		hooktest.Stdin(t, Definition(), hooktest.BashPayload(command, "/tmp", nil))
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("pathological inputs took %s", elapsed)
	}
}

// Bash 以外の payload が来ても落ちない（matcher の設定漏れへの保険）。
func TestOtherToolPayload(t *testing.T) {
	raw, err := json.Marshal(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": "/tmp/x"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := hooktest.Stdin(t, Definition(), string(raw)); got.Decision != "" {
		t.Fatalf("other tool: %+v", got)
	}
}

// 開錠の対照に使う各ルールの代表例。
var unlockCases = []string{
	"gh pr merge 123",
	"gh api -X PUT repos/o/r/pulls/1/merge",
	"gh api graphql -f query='mutation { mergePullRequest(input: {}) { clientMutationId } }'",
	"curl -X PUT https://api.github.com/repos/o/r/pulls/1/merge",
}

func TestBlockedWithoutUnlock(t *testing.T) {
	t.Setenv(UnlockEnv, "")
	check(t, hooktest.Deny, hooktest.Commands(unlockCases...))
}

func TestUnlockedSessionPassesEverything(t *testing.T) {
	t.Setenv(UnlockEnv, "1")
	check(t, "", hooktest.Commands(unlockCases...))
	if got := hooktest.Argv(t, Definition(), "gh pr merge 1"); got.Decision != "" {
		t.Fatalf("argv path must also be unlocked: %+v", got)
	}
}

// "1" 以外は開錠しない（空文字列や 0 の取り違えで開かないこと）。
func TestOtherValuesDoNotUnlock(t *testing.T) {
	for _, value := range []string{"", "0", "2", "true", "yes", " 1"} {
		t.Setenv(UnlockEnv, value)
		check(t, hooktest.Deny, hooktest.Commands("gh pr merge 1"))
	}
}

func TestDisabledByConfig(t *testing.T) {
	got := hooktest.Run(t, Definition(), hooktest.BashPayload("gh pr merge 1", "/tmp", nil), hooktest.Options{
		Config: "hooks:\n  pr-merge-guard:\n    enabled: false\n",
	})
	if got.Decision != "" {
		t.Fatalf("disabled hook must write nothing: %+v", got)
	}
}
