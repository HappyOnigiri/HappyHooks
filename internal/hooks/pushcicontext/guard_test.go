package pushcicontext

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HappyOnigiri/hhx/internal/hookrt"
	"github.com/HappyOnigiri/hhx/internal/hooktest"
	"github.com/HappyOnigiri/hhx/internal/i18n"
)

func TestMain(m *testing.M) {
	os.Exit(hooktest.Main(m))
}

const (
	claudeTranscript = "/tmp/agent/projects/test-session.jsonl"
	codexTranscript  = "/tmp/agent/sessions/rollout-2026-09-19T00-00-00-test-session.jsonl"
)

// call は payload の組み立ての条件である。零値は Claude Code の PostToolUse で、tool_response は空のオブジェクトになる。
type call struct {
	response any
	cwd      string
	event    string
	codex    bool
}

func payload(command string, options call) string {
	event := options.event
	if event == "" {
		event = "PostToolUse"
	}
	transcript := claudeTranscript
	if options.codex {
		transcript = codexTranscript
	}
	data := map[string]any{
		"session_id":      "test-session",
		"transcript_path": transcript,
		"hook_event_name": event,
		"tool_name":       "Bash",
		"tool_input":      map[string]any{"command": command},
		"cwd":             options.cwd,
	}
	if event == "PostToolUse" {
		data["tool_response"] = map[string]any{}
		if options.response != nil {
			data["tool_response"] = options.response
		}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// inject は stdin の payload で起動し、注入された文面を返す。注入が無ければ ok は偽になる。
func inject(t *testing.T, command string, options call) (string, bool) {
	t.Helper()
	return injectRaw(t, payload(command, options), options.event)
}

func injectRaw(t *testing.T, raw, event string) (string, bool) {
	t.Helper()
	if event == "" {
		event = "PostToolUse"
	}
	injection := hooktest.ParseInjection(t, hooktest.Output(t, Definition(), raw, hooktest.Options{}))
	if injection.SystemMessage != "" {
		t.Fatalf("unexpected systemMessage: %q", injection.SystemMessage)
	}
	if injection.Event == "" {
		return "", false
	}
	if injection.Event != event {
		t.Fatalf("hookEventName=%q, want %q", injection.Event, event)
	}
	return injection.Context, true
}

type repositories struct {
	github, other, plain string
}

func newRepositories(t *testing.T) repositories {
	t.Helper()
	root := hooktest.TempDir(t)
	plain := filepath.Join(root, "plain")
	if err := os.Mkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	repos := repositories{
		github: hooktest.GitRepo(t, filepath.Join(root, "github"), "https://github.com/owner/repo.git", false),
		other:  hooktest.GitRepo(t, filepath.Join(root, "gitlab"), "git@gitlab.com:owner/repo.git", false),
		plain:  plain,
	}
	hooktest.Git(t, repos.github, "symbolic-ref", "HEAD", "refs/heads/feature")
	return repos
}

func mustContain(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if !strings.Contains(text, part) {
			t.Errorf("%q is missing from:\n%s", part, text)
		}
	}
}

func mustNotContain(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if strings.Contains(text, part) {
			t.Errorf("%q must not appear in:\n%s", part, text)
		}
	}
}

func TestPushInjectsBashGuidance(t *testing.T) {
	repos := newRepositories(t)
	context, ok := inject(t, "git push origin HEAD", call{cwd: repos.github})
	if !ok {
		t.Fatal("push must inject")
	}
	mustContain(t, context, messages.T(hooktest.Language, idPostPush),
		messages.Text(hooktest.Language, idGuidance, map[string]any{"Command": waitCommand, "Order": messages.T(hooktest.Language, idOrder)}),
		"timeout:600000", "`hhx wait-ci --progress`\n")
	mustNotContain(t, context, "functions.exec", "write_stdin")
}

func TestMainPushDoesNotInject(t *testing.T) {
	repos := newRepositories(t)
	hooktest.Git(t, repos.github, "symbolic-ref", "HEAD", "refs/heads/main")
	for _, testCase := range []struct {
		command  string
		response any
	}{
		{"git push", nil},
		{"git push origin HEAD", nil},
		{"git push origin main", nil},
		{"git push origin feature:main", nil},
		{"git push origin HEAD", map[string]any{"stderr": "To github.com:owner/repo.git\n   abc..def  main -> main\n"}},
	} {
		if context, ok := inject(t, testCase.command, call{cwd: repos.github, response: testCase.response}); ok {
			t.Errorf("%q injected %q", testCase.command, context)
		}
	}
	for _, command := range []string{"git push origin feature", "git push origin HEAD:feature", "gh pr create --fill"} {
		if _, ok := inject(t, command, call{cwd: repos.github}); !ok {
			t.Errorf("%q must inject", command)
		}
	}
	if _, ok := inject(t, "git push origin main", call{cwd: repos.github,
		response: map[string]any{"stderr": "To github.com:owner/repo.git\n   abc..def  feature -> feature\n"}}); !ok {
		t.Fatal("push result must take precedence over the command")
	}
}

func TestStashPushDoesNotInject(t *testing.T) {
	repos := newRepositories(t)
	for _, command := range []string{
		"git stash push",
		"git stash push -m checkpoint",
		"git -C /other/worktree stash push",
		"GIT_TRACE=1 /usr/bin/git stash push",
	} {
		for _, codex := range []bool{false, true} {
			if context, ok := inject(t, command, call{cwd: repos.github, codex: codex}); ok {
				t.Errorf("%q (codex=%v) injected %q", command, codex, context)
			}
		}
	}
	// 先行する stash の push を、後続の実際の push と取り違えて main 判定しない。
	hooktest.Git(t, repos.github, "symbolic-ref", "HEAD", "refs/heads/main")
	if _, ok := inject(t, "git stash push; git push origin feature", call{cwd: repos.github}); !ok {
		t.Fatal("a feature push after stash must inject even on main")
	}
}

func TestCodexGetsTheCodeModeWaitLoop(t *testing.T) {
	repos := newRepositories(t)
	context, _ := inject(t, "git push origin HEAD", call{cwd: repos.github, codex: true})
	mustContain(t, context, `"yield_time_ms":3600000`, "write_stdin", "exit_code",
		`tools.exec_command({cmd:"hhx wait-ci --progress",yield_time_ms:1000})`,
		messages.Text(hooktest.Language, idCodex, map[string]any{"Label": messages.T(hooktest.Language, idLabelWaitCI), "Command": `"hhx wait-ci --progress"`}))
	mustNotContain(t, context, "timeout:600000")
}

func TestClaudeDoesNotInheritCodexInstructions(t *testing.T) {
	repos := newRepositories(t)
	t.Setenv("CODEX_THREAD_ID", "test-session")
	context, _ := inject(t, "git push origin HEAD", call{cwd: repos.github})
	mustNotContain(t, context, "functions.exec", "functions.wait")
}

func TestWaitIsOrderedAfterRemainingWork(t *testing.T) {
	repos := newRepositories(t)
	for command, response := range map[string]any{
		"git push origin HEAD":                  nil,
		"gh workflow run deploy.yml --ref main": map[string]any{"stdout": "https://github.com/owner/repo/actions/runs/123456789"},
	} {
		context, _ := inject(t, command, call{cwd: repos.github, response: response})
		mustContain(t, context, messages.T(hooktest.Language, idOrder))
	}
}

func TestPRCreateInjects(t *testing.T) {
	repos := newRepositories(t)
	if _, ok := inject(t, "gh pr create --fill --draft", call{cwd: repos.github}); !ok {
		t.Fatal("gh pr create must inject")
	}
}

func TestWorkflowDispatch(t *testing.T) {
	repos := newRepositories(t)
	context, _ := inject(t, "gh workflow run deploy.yml --ref main", call{cwd: repos.github, codex: true,
		response: map[string]any{"stdout": "https://github.com/owner/repo/actions/runs/123456789"}})
	mustContain(t, context, messages.T(hooktest.Language, idPostDispatch), "gh run watch 123456789 --exit-status",
		`"yield_time_ms":3600000`, "write_stdin",
		messages.Text(hooktest.Language, idDispatchGuidance, map[string]any{"Order": messages.T(hooktest.Language, idOrder)}),
		messages.Text(hooktest.Language, idCodex, map[string]any{
			"Label": messages.T(hooktest.Language, idLabelDispatch), "Command": `"gh run watch 123456789 --exit-status"`,
		}))
	mustNotContain(t, context, "wait-ci --progress")

	context, ok := inject(t, "gh workflow run deploy.yml -R owner/repo", call{cwd: repos.plain, codex: true,
		response: map[string]any{"stdout": "https://github.com/owner/repo/actions/runs/987654321"}})
	if !ok {
		t.Fatal("dispatch with -R must inject outside a repository")
	}
	mustContain(t, context, "gh run watch 987654321 --exit-status -R owner/repo")
}

func TestWorkflowDispatchWithoutURLPollsInOneCommand(t *testing.T) {
	repos := newRepositories(t)
	context, _ := inject(t, "gh workflow run deploy.yml --ref main -R owner/repo", call{cwd: repos.plain, codex: true,
		response: map[string]any{"stdout": "✓ Created"}})
	mustContain(t, context, "gh run list --event workflow_dispatch", "--workflow deploy.yml", "--branch main", "sleep 30",
		messages.T(hooktest.Language, idMultipleCandidates), messages.T(hooktest.Language, idRegistrationTimeout),
		"list_rc", "gh run watch", "-R owner/repo")
	if count := strings.Count(context, "tools.exec_command"); count != 1 {
		t.Errorf("tools.exec_command appears %d times", count)
	}

	context, _ = inject(t, "gh workflow run deploy.yml --ref main", call{cwd: repos.github,
		response: map[string]any{"stdout": "Created without a URL"}})
	mustContain(t, context, "gh run list --event workflow_dispatch", "sleep 30", "timeout:600000")
}

func TestSilentCases(t *testing.T) {
	repos := newRepositories(t)
	for name, testCase := range map[string]struct {
		command string
		options call
	}{
		"failed dispatch":        {"gh workflow run deploy.yml", call{cwd: repos.github, response: map[string]any{"exit_code": 1, "stderr": "HTTP 422"}}},
		"rejected push":          {"git push origin HEAD", call{cwd: repos.github, response: map[string]any{"stderr": "! [rejected]        main -> main (non-fast-forward)"}}},
		"nonzero exit":           {"git push origin HEAD", call{cwd: repos.github, response: map[string]any{"exit_code": 1, "stdout": ""}}},
		"up to date":             {"git push", call{cwd: repos.github, response: map[string]any{"stderr": "Everything up-to-date"}}},
		"non-GitHub origin":      {"git push origin HEAD", call{cwd: repos.other}},
		"ls":                     {"ls -la", call{cwd: repos.github}},
		"npm script":             {"npm run push-notifications", call{cwd: repos.github}},
		"git status":             {"git status", call{cwd: repos.github}},
		"dry run before push":    {"git push --dry-run", call{cwd: repos.github, event: "PreToolUse"}},
		"unknown event":          {"git push origin HEAD", call{cwd: repos.github, event: "UserPromptSubmit"}},
		"unclosed quote":         {"git push 'origin", call{cwd: repos.github}},
		"non-GitHub origin (PR)": {"gh pr create --fill", call{cwd: repos.other}},
	} {
		if context, ok := inject(t, testCase.command, testCase.options); ok {
			t.Errorf("%s: must be silent, injected:\n%s", name, context)
		}
	}
}

func TestGitHubPushOutputBeatsANonGitHubCwd(t *testing.T) {
	repos := newRepositories(t)
	context, ok := inject(t, "git push origin HEAD", call{cwd: repos.other,
		response: map[string]any{"exit_code": 0, "stderr": "To github.com:owner/repo.git\n   abc..def  feature -> feature\n"}})
	if !ok {
		t.Fatal("a push to GitHub must inject even from a non-GitHub cwd")
	}
	mustContain(t, context, "wait-ci")
}

func TestPushFromAWorkspaceRootInjects(t *testing.T) {
	repos := newRepositories(t)
	_, ok := inject(t, "git status --short --branch && git push -u origin fix/x", call{cwd: repos.plain,
		response: map[string]any{"exit_code": 0, "output": "## fix/x\nTo github.com:owner/repo\n * [new branch]      fix/x -> fix/x\n"}})
	if !ok {
		t.Fatal("a push reported as GitHub must inject from a workspace root")
	}
	if _, ok := inject(t, "git push origin HEAD", call{cwd: repos.plain}); !ok {
		t.Fatal("an undeterminable repository must inject")
	}
	if _, ok := inject(t, "git push origin HEAD", call{cwd: filepath.Join(repos.plain, "missing")}); !ok {
		t.Fatal("a missing cwd must inject")
	}
}

func TestPreToolUseInjectsBeforeThePush(t *testing.T) {
	repos := newRepositories(t)
	context, ok := inject(t, "git push origin HEAD", call{cwd: repos.github, event: "PreToolUse"})
	if !ok {
		t.Fatal("PreToolUse must inject")
	}
	mustContain(t, context, messages.T(hooktest.Language, idPrePush), "wait-ci")
	context, _ = inject(t, "gh workflow run deploy.yml", call{cwd: repos.github, event: "PreToolUse"})
	mustContain(t, context, messages.T(hooktest.Language, idPreDispatch))
}

func TestStrangePayloadsAreSilent(t *testing.T) {
	for _, raw := range []string{
		"not json but mentions push",
		"", "null", "[]", `"git push"`, "{}",
		`{"tool_input": "git push"}`,
		`{"tool_input": ["git push"]}`,
		`{"tool_input": {"command": ["git", "push"]}}`,
		`{"tool_input": {"command": 1}, "x": "push"}`,
		`{"tool_input": {"command": "git push"}, "hook_event_name": ["PostToolUse"]}`,
		`{"tool_input": {"command": "git push"}, "hook_event_name": 1}`,
		`{"tool_input": {"command": "git push"}, "cwd": 5}`,
		`{"tool_input": {"command": "git push"}} trailing`,
		"{\"tool_input\": {\"command\": \"git push\"}, \"x\": \"\xff\"}",
	} {
		if context, ok := injectRaw(t, raw, ""); ok {
			t.Errorf("payload %q must be silent, injected:\n%s", raw, context)
		}
	}
}

func TestMissingFieldsFallBackLikeThePythonImplementation(t *testing.T) {
	// hook_event_name が無ければ PostToolUse、cwd が無ければプロセスの作業ディレクトリ（git 管理外）で判定する。
	context, ok := injectRaw(t, `{"tool_input": {"command": "git push"}, "tool_response": {"stdout": "x"}}`, "PostToolUse")
	if !ok {
		t.Fatal("a payload without the event and cwd must inject")
	}
	mustContain(t, context, messages.T(hooktest.Language, idPostPush))
}

func TestArgvPath(t *testing.T) {
	output := hooktest.Output(t, Definition(), "", hooktest.Options{Args: []string{"git push origin HEAD"}})
	injection := hooktest.ParseInjection(t, output)
	if injection.Event != "PostToolUse" || !strings.Contains(injection.Context, "timeout:600000") {
		t.Fatalf("argv must be treated as a successful PostToolUse: %+v", injection)
	}
	for _, command := range []string{"git status", "ls", "git stash push", "git -C /other/worktree stash push"} {
		if got := hooktest.Output(t, Definition(), "", hooktest.Options{Args: []string{command}}); got != "" {
			t.Errorf("argv %q must be silent: %q", command, got)
		}
	}
}

func TestDisabledByConfig(t *testing.T) {
	output := hooktest.Output(t, Definition(), payload("git push", call{}),
		hooktest.Options{Config: "hooks:\n  push-ci-context:\n    enabled: false\n"})
	if output != "" {
		t.Fatalf("a disabled hook must be silent: %q", output)
	}
}

func TestGate(t *testing.T) {
	for input, want := range map[string]bool{
		"git PUSH": true, "gh pr create": true, "gh PR CREATE": true, "gh workflow run": true,
		"ls": false, "gh pr view": false, "workflow": false, "gh create": false,
	} {
		if got := gate([]byte(input)); got != want {
			t.Errorf("gate(%q)=%v, want %v", input, got, want)
		}
	}
}

func TestTriggers(t *testing.T) {
	for _, command := range []string{
		"git push",
		"git push origin HEAD",
		"git push --force-with-lease origin feature",
		"git push origin 61c6a6b:refs/heads/feature",
		"git -C /other/worktree push origin HEAD",
		"git -C push -c advice.pushUpdateRejected=false --no-pager push origin HEAD",
		"git -C/other/worktree -cadvice.pushUpdateRejected=false push origin HEAD",
		"git --git-dir push --work-tree /other/worktree push origin HEAD",
		"git --git-dir=/other/repo.git --config-env=advice.pushUpdateRejected=ADVICE push origin HEAD",
		"git stash push; git push origin feature",
		"/usr/bin/git push",
		"make build && git push",
		"git commit -m 'x'; git push",
		"gh pr create --fill",
		"gh pr create --draft --title x --body y",
		"gh workflow run deploy.yml",
		"gh workflow run deploy.yml --ref main -f target=staging",
		"gh workflow run deploy.yml -f 'message=a;b'",
		"/usr/local/bin/gh workflow run deploy.yml -R owner/repo",
		"gh workflow   run deploy.yml",
		"GIT_TRACE=1 git push",
		"true\ngit push",
		"gh PR Create",
	} {
		if triggerKind(command) == none {
			t.Errorf("%q must trigger CI", command)
		}
	}
	for _, command := range []string{
		"git push --dry-run origin HEAD",
		"git push -n origin HEAD",
		"git push origin --delete feature",
		"git push origin :feature",
		"git status",
		"git fetch origin",
		"git stash push",
		"git -C /other/worktree stash push",
		"git log --oneline push",
		"git -C push status",
		"git -c push status",
		"git --git-dir push status",
		"git --config-env push status",
		"git -C push",
		"gh pr view 155",
		"gh pr checks 155",
		"gh workflow run --help",
		"gh workflow view deploy.yml",
		"gh run list --workflow deploy.yml",
		"echo 'gh workflow run deploy.yml'",
		"rg 'gh workflow run' .",
		"echo push",
		"npm run push-image",
		"git push 'unterminated",
		`git push \`,
		"",
	} {
		if kind := triggerKind(command); kind != none {
			t.Errorf("%q must not trigger CI (kind %d)", command, kind)
		}
	}
	if triggerKind("gh workflow run x") != workflowDispatch || triggerKind("gh pr create") != pushOrPR {
		t.Error("the trigger kinds are mixed up")
	}
}

func TestCommandSegments(t *testing.T) {
	got := commandSegments("a 'b;c' d;e&&f|g\nh ';' i")
	want := [][]string{{"a", "b;c", "d"}, {"e"}, {"f"}, {"g"}, {"h"}, {"i"}}
	if len(got) != len(want) {
		t.Fatalf("segments=%q, want %q", got, want)
	}
	for index := range want {
		if strings.Join(got[index], "\x00") != strings.Join(want[index], "\x00") {
			t.Fatalf("segments=%q, want %q", got, want)
		}
	}
}

func TestIsCodex(t *testing.T) {
	for _, payload := range []map[string]any{
		{},
		{"session_id": "id", "transcript_path": nil},
		{"session_id": "id", "transcript_path": "/tmp/rollout-date-other.jsonl"},
		{"session_id": "", "transcript_path": "/tmp/rollout-date-.jsonl"},
		{"session_id": 1, "transcript_path": "/tmp/rollout-date-1.jsonl"},
	} {
		if isCodex(payload) {
			t.Errorf("%v must not be Codex", payload)
		}
	}
	if !isCodex(map[string]any{"session_id": "id", "transcript_path": "/x/rollout-2026-id.jsonl"}) {
		t.Error("a rollout transcript of the session must be Codex")
	}
}

func TestWorkflowWatchCommand(t *testing.T) {
	saved := now
	now = func() time.Time { return time.Date(2026, 9, 19, 1, 2, 3, 0, time.UTC) }
	t.Cleanup(func() { now = saved })
	got := workflowWatchCommand(hooktest.Language, "gh workflow run 'deploy it.yml' -r feat/x -R 'o/r' --json -f a=b", map[string]any{"stdout": "Created"})
	mustContain(t, got, "gh run list --event workflow_dispatch --workflow 'deploy it.yml' --branch feat/x --limit 20 "+
		"--json databaseId,createdAt --jq 'map(select(.createdAt >= \"2026-09-19T01:01:03Z\")) | .[].databaseId' -R o/r",
		`gh run watch "$run_id" --exit-status -R o/r`)
	if got := workflowWatchCommand(hooktest.Language, "gh workflow run x --repo=o/r", map[string]any{"stdout": "/actions/runs/42?x"}); got != "gh run watch 42 --exit-status -R o/r" {
		t.Errorf("run URL: %q", got)
	}
	if got := workflowWatchCommand(hooktest.Language, "gh workflow run x -Ro/r", map[string]any{"output": "/actions/runs/7"}); got != "gh run watch 7 --exit-status -R o/r" {
		t.Errorf("attached -R: %q", got)
	}
	if got := workflowWatchCommand(hooktest.Language, "gh workflow run x", map[string]any{"stdout": "/actions/runs/7x"}); strings.HasPrefix(got, "gh run watch 7") {
		t.Errorf("a run ID must end at a URL boundary: %q", got)
	}
	if name, ok := workflowName([]string{"gh", "workflow", "run", "-R", "o/r", "--json", "-F", "a=b", "--flag", "wf"}); !ok || name != "wf" {
		t.Errorf("workflowName=%q", name)
	}
	if _, ok := workflowName([]string{"gh", "workflow", "run"}); ok {
		t.Error("no workflow name must be found")
	}
}

// fakeRunList は gh run list の代わりに body を実行する gh を PATH の先頭に置き、登録待ちのループを bash で走らせる。
func fakeRunList(t *testing.T, body string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	command := workflowWatchCommand(hooktest.Language, "gh workflow run deploy.yml --ref main", map[string]any{"stdout": "Created"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process := exec.CommandContext(ctx, "bash", "-c", command)
	process.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stderr strings.Builder
	process.Stderr = &stderr
	err := process.Run()
	if ctx.Err() != nil {
		t.Fatal("the wait loop did not stop")
	}
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	}
	return code, stderr.String()
}

func TestWaitLoopStopsOnMultipleCandidates(t *testing.T) {
	code, stderr := fakeRunList(t, "printf '111\\n222\\n'\n")
	if code != 2 || !strings.Contains(stderr, messages.T(hooktest.Language, idMultipleCandidates)) {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestWaitLoopPropagatesRunListFailure(t *testing.T) {
	if code, _ := fakeRunList(t, "exit 7\n"); code != 7 {
		t.Fatalf("code=%d, want 7", code)
	}
}

// Codex は additionalContextLimit を超えた注入を切る。英語は日本語より長くなりやすいので、両言語の最長の案内で確かめる。
func TestGuidanceFitsTheCodexContextLimit(t *testing.T) {
	limit := 0
	for _, registration := range Definition().Registrations {
		if registration.Agent == hookrt.Codex {
			limit = registration.AdditionalContextLimit
		}
	}
	if limit == 0 {
		t.Fatal("the Codex registration must set additionalContextLimit")
	}
	// run ID の無い dispatch が最長になる（登録確認ループのコマンドを JSON 文字列で埋め込む）。
	long := "gh workflow run " + strings.Repeat("w", 100) + ".yml --ref " + strings.Repeat("r", 100) + " -R owner/repo"
	for _, language := range []i18n.Language{i18n.English, i18n.Japanese} {
		for _, input := range []*invocation{
			{command: "git push origin HEAD", event: "PostToolUse", codex: true},
			{command: long, event: "PostToolUse", codex: true, response: map[string]any{"stdout": "Created"}},
			{command: long, event: "PreToolUse", codex: true},
		} {
			text := guidanceText(language, input, triggerKind(input.command))
			if len(text) > limit {
				t.Errorf("%s: %q is %d bytes, over the limit %d", language, input.command, len(text), limit)
			}
		}
	}
}
