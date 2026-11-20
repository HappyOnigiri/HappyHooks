package prbodyeditguard

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/HappyOnigiri/hhx/internal/hooktest"
)

func TestMain(m *testing.M) {
	os.Exit(hooktest.Main(m))
}

const (
	current = "# Summary\n\nPrevious text\n\n## Review\n- [ ] Ready"
	hint    = "hint: subscribe to changes instead of polling:\n" +
		"    gh event listen --resource https://github.com/example/widget/pull/12\n" +
		"  - It exits after printing the event.\n" +
		"  - Use --timeout to stop waiting."
	logs    = "direnv: loading ./example/.envrc\ndirenv: export +EXAMPLE"
	color   = "\x1b[0mdirenv: loading ./example/.envrc"
	command = "gh pr edit 12 -R example/widget --body-file 'proposed body.md'"
)

func TestEdgeDiagnostics(t *testing.T) {
	edited := strings.Replace(current, "Previous text", "Updated text", 1)
	for _, tc := range []struct {
		name, original, body string
		want                 []finding
	}{
		{"guidance at start", current, hint + "\n" + edited, []finding{{1, 4, idGuidance, idHead}}},
		{"ansi at start", current, color + "\n" + edited, []finding{{1, 1, idANSI, idHead}}},
		{"logs at end", current, edited + "\n\n" + logs, []finding{{8, 9, idLogs, idTail}}},
		{
			"both edges", current, hint + "\n" + edited + "\n" + color,
			[]finding{{1, 4, idGuidance, idHead}, {11, 11, idANSI, idTail}},
		},
		{"changes between anchors", current, edited + "\n\n## Additional notes\nSee the updated tests.", nil},
		{"ordinary preface and appendix", current, "Warning: deployment requires approval.\n\n" + edited + "\n\n- Verify CI.", nil},
		{"guidance in existing section", current, strings.Replace(edited, "Updated text", hint, 1), nil},
		{"existing diagnostic unchanged", color + "\n" + current, color + "\n" + edited, nil},
		{
			"detected terminal bytes in a code fence", current, edited + "\n```text\n" + color + "\n```",
			[]finding{{8, 8, idANSI, idTail}},
		},
		{"quoted log", current, "> " + strings.ReplaceAll(logs, "\n", "\n> ") + "\n\n" + edited, nil},
		{"fenced log", current, edited + "\n\n~~~text\n" + hint + "\n~~~", nil},
		{"log after a fence", current, edited + "\n~~~text\n" + hint + "\n~~~\n" + logs, []finding{{13, 14, idLogs, idTail}}},
		{"markdown ANSI example", current, edited + "\n\n```text\n\\x1b[0m\n```", nil},
		{"distinct diagnostic prefixes", current, edited + "\nformatter: starting\nlinter: starting", nil},
		{"updated first line has no anchor", current, color + "\n" + strings.Replace(edited, "# Summary", "# New title", 1), nil},
		{"repeated anchor", current, color + "\n" + edited + "\n# Summary", nil},
		{"reordered anchors", current, "- [ ] Ready\n" + hint + "\n# Summary", nil},
		{"empty baseline", "", color + "\n" + edited, nil},
		{
			"CRLF", "\n" + strings.ReplaceAll(current, "\n", "\r\n") + "\r\n",
			"\r\n" + strings.ReplaceAll(hint+"\n"+edited, "\n", "\r\n"),
			[]finding{{2, 5, idGuidance, idHead}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := splitLines(tc.body)
			got := edgeFindings(tc.original, lines, excludedLines(lines))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("findings = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestPreToolUse(t *testing.T) {
	gh := hooktest.NewFakeGH(t)
	gh.Write(t, "pr_example_widget_12.json", map[string]string{"body": current})
	dir := hooktest.TempDir(t)
	file := filepath.Join(dir, "proposed body.md")
	proposed := hint + "\n" + current
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw := func(cmd string) string {
		return hooktest.BashPayload(cmd, "/other", map[string]any{"workdir": dir})
	}
	run := func(cmd string) hooktest.Result {
		return hooktest.Stdin(t, Definition(), raw(cmd))
	}
	write(proposed)
	// 端末の出力を含む本文に対して、同じ PR の本文を読み、行範囲と ACK を返す。
	want := reason(hooktest.Language, []finding{{1, 4, idGuidance, idHead}}, bodyDigest(proposed))
	if got := run(command); got.Decision != hooktest.Deny || got.Reason != want {
		t.Fatalf("got %#v, want deny with %q", got, want)
	}
	calls := gh.Calls(t)
	wantArgs := []string{"pr", "view", "12", "--repo", "example/widget", "--json", "body"}
	if len(calls) != 1 || calls[0].Cwd != dir || !reflect.DeepEqual(calls[0].Argv, wantArgs) {
		t.Fatalf("gh calls = %#v, want %#v in %s", calls, wantArgs, dir)
	}

	gh.ClearCalls(t)
	ack := "HHX_PR_BODY_ACK=" + bodyDigest(proposed) + " " + command
	if got := run(ack); got.Decision != "" || len(gh.Calls(t)) != 0 {
		t.Fatalf("valid ACK should pass without fetching: %#v, %#v", got, gh.Calls(t))
	}
	t.Setenv(ackVariable, bodyDigest(proposed))
	if got := run(command); got.Decision != hooktest.Deny {
		t.Fatalf("the hook process environment must not acknowledge the command: %#v", got)
	}
	write(proposed + "\n")
	if got := run(ack); got.Decision != hooktest.Deny {
		t.Fatalf("ACK for old bytes accepted: %#v", got)
	}
	if got := run("HHX_PR_BODY_ACK=1 " + command); got.Decision != hooktest.Deny {
		t.Fatalf("fixed ACK accepted: %#v", got)
	}
	gh.SetMode(t, "fail")
	if got := run(command); got.Decision != "" {
		t.Fatalf("a failed lookup must fail open: %#v", got)
	}
	gh.SetMode(t, "garbage")
	if got := run(command); got.Decision != "" {
		t.Fatalf("invalid GitHub response must fail open: %#v", got)
	}
	gh.SetMode(t, "ok")
	gh.ClearCalls(t)
	write("# Summary\n\nRevised text.\n\n## Review\n- [ ] Ready")
	if got := run(command); got.Decision != "" || len(gh.Calls(t)) != 0 {
		t.Fatalf("ordinary edits should pass without fetching: %#v, %#v", got, gh.Calls(t))
	}
	write(proposed)
	got := hooktest.Run(t, Definition(), raw(command), hooktest.Options{
		Config: "hooks:\n  pr-body-edit-guard:\n    enabled: false\n",
	})
	if got.Decision != "" || len(gh.Calls(t)) != 0 {
		t.Fatalf("disabled hook should pass without fetching: %#v, %#v", got, gh.Calls(t))
	}
}

func TestLiteralEditCommands(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    editRequest
	}{
		{command, editRequest{dir: "/workspace", selector: "12", repo: "example/widget", bodyFile: "proposed body.md"}},
		{"gh pr edit --body-file=body.md", editRequest{dir: "/workspace", bodyFile: "body.md"}},
		{
			"gh pr edit https://github.com/example/widget/pull/12 -Fbody.md --title Title",
			editRequest{dir: "/workspace", selector: "https://github.com/example/widget/pull/12", bodyFile: "body.md"},
		},
		{
			"cd '/workspace/app' && gh pr edit -Rexample/widget -b 'line one\nline two' -- topic/branch",
			editRequest{dir: "/workspace/app", selector: "topic/branch", repo: "example/widget", bodyText: "line one\nline two"},
		},
		{"gh pr edit 12 -F body.md && gh pr view 12", editRequest{dir: "/workspace", selector: "12", bodyFile: "body.md"}},
		{"cd ./app && gh pr edit 12 -F body.md", editRequest{dir: "/workspace/./app", selector: "12", bodyFile: "body.md"}},
		{
			"cd '/workspace/app' && gh pr edit 12 -F body.md && gh pr view 12",
			editRequest{dir: "/workspace/app", selector: "12", bodyFile: "body.md"},
		},
	} {
		got, ok := parseEdit(tc.command, "/workspace")
		if !ok || got != tc.want {
			t.Errorf("%s: got %#v, %v; want %#v", tc.command, got, ok, tc.want)
		}
	}
	for _, command := range []string{
		"", "gh pr edit 12 --body-file", "gh pr edit 12 --body-file -", "gh pr edit 12 --title New",
		"gh pr edit 12 --body-file body.md --body another", "gh pr edit 12 --body-file \"$file\"",
		"gh pr create --body-file body.md", "echo gh pr edit 12 --body-file body.md",
		"cd elsewhere && gh pr edit 12 -F body.md", "GH_REPO=example/other gh pr edit 12 -F body.md",
	} {
		if _, ok := parseEdit(command, "/workspace"); ok {
			t.Errorf("%s: unexpectedly recognized", command)
		}
	}
}

func TestLogicalCD(t *testing.T) {
	gh := hooktest.NewFakeGH(t)
	gh.Write(t, "pr_example_widget_12.json", map[string]string{"body": current})
	dir := hooktest.TempDir(t)
	physical := hooktest.TempDir(t)
	nested := filepath.Join(physical, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "alias")
	if err := os.Symlink(nested, link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(physical, "proposed body.md"), []byte(current), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "proposed body.md"), []byte(hint+"\n"+current), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, cmd := range []string{
		"cd ./alias/.. && " + command,
		"cd '" + link + "/..' && " + command + " && gh pr view 12",
	} {
		got := hooktest.Stdin(t, Definition(), hooktest.BashPayload(cmd, dir, nil))
		if got.Decision != hooktest.Deny {
			t.Errorf("%s: got %#v, want deny from the logical directory", cmd, got)
		}
	}
	for _, call := range gh.Calls(t) {
		if call.Cwd != dir {
			t.Errorf("gh cwd = %s, want %s", call.Cwd, dir)
		}
	}
	if _, ok := parseEdit("cd ./missing/.. && "+command, dir); ok {
		t.Error("cd through a missing directory was recognized")
	}
	if _, ok := parseEdit("cd .. && "+command, link); ok {
		t.Error("relative cd with an ambiguous logical PWD was recognized")
	}
}

func TestCurrentBranchWithRepository(t *testing.T) {
	gh := hooktest.NewFakeGH(t)
	gh.Write(t, "pr_example_widget_current.json", map[string]string{"body": current})
	t.Setenv("GH_REPO", "example/other")
	dir := hooktest.TempDir(t)
	edit := "gh pr edit --repo example/widget --body '" + color + "\n" + current + "'"
	got := hooktest.Stdin(t, Definition(), hooktest.BashPayload(edit, dir, nil))
	if got.Decision != hooktest.Deny {
		t.Fatalf("got %#v, want deny", got)
	}
	want := []hooktest.FakeGHCall{{Argv: []string{"pr", "view", "--json", "body"}, Cwd: dir, Repo: "example/widget"}}
	if calls := gh.Calls(t); !reflect.DeepEqual(calls, want) {
		t.Fatalf("gh calls = %#v, want %#v", calls, want)
	}
	if repo := os.Getenv("GH_REPO"); repo != "example/other" {
		t.Errorf("process GH_REPO = %q", repo)
	}
}
