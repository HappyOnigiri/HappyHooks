package prbodyroundtrip

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/HappyOnigiri/hhx/internal/hooktest"
)

func TestMain(m *testing.M) {
	os.Exit(hooktest.Main(m))
}

const (
	editCommand = "gh pr edit 42 -R example/widget --body-file 'sent body.md'"
	prURL       = "https://github.com/example/widget/pull/42"
	sentBody    = "# Context\n\nExpected line\n"
	current     = "# Context\n\nPublished line"
)

func testPayload(t *testing.T, cwd, command string, response any) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"hook_event_name": "PostToolUse",
		"tool_name":       "Bash",
		"cwd":             cwd,
		"tool_use_id":     "repl_00000000",
		"tool_input":      map[string]string{"command": command},
		"tool_response":   response,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeFile(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPostToolUse(t *testing.T) {
	t.Setenv("GH_REPO", "")
	status := map[string]any{"stdout": "rc=0\n", "stderr": "", "interrupted": false}
	for _, tc := range []struct {
		name, command, sent, current string
		response                     any
		args                         []string
		repo                         string
		cd                           bool
	}{
		{
			name: "equivalent", command: editCommand,
			sent: "# Context\r\n\r\nExpected line\r\n\r\n", current: sentBody,
			response: map[string]any{},
			args:     []string{"42", "--repo", "example/widget"},
		},
		{
			name: "edit with number", command: editCommand, sent: sentBody, current: current,
			response: map[string]any{"exit_code": 0},
			args:     []string{"42", "--repo", "example/widget"},
		},
		{
			name: "edit with url and status", command: "gh pr edit " + prURL +
				" --body-file 'sent body.md' > './output file' 2>&1; echo rc=$?",
			sent: sentBody, current: current, response: status,
			args: []string{prURL},
		},
		{
			name: "edit after cd", command: "cd './elsewhere' && gh pr edit -Rexample/widget -F='sent body.md'",
			sent: sentBody, current: current, response: map[string]any{},
			args: []string{}, cd: true, repo: "example/widget",
		},
		{
			name: "edit with branch", command: "HHX_PR_BODY_ACK=abc gh pr edit -F'sent body.md' --repo=example/widget -- branch",
			sent: sentBody, current: current, response: prURL,
			args: []string{"branch", "--repo", "example/widget"},
		},
		{
			name: "create using stdout", command: "gh pr create -R example/widget --title 'A new widget' --base main " +
				"--head branch --body-file='sent body.md' -d",
			sent: sentBody, current: current, response: map[string]any{"stdout": prURL + "\n", "stderr": "notice"},
			args: []string{prURL},
		},
		{
			name: "create using combined output", command: "gh pr create -F 'sent body.md' --fill --draft=false",
			sent: sentBody, current: current, response: map[string]any{"output": "notice\n" + prURL + "\n"},
			args: []string{prURL},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gh := hooktest.NewFakeGH(t)
			dir := hooktest.TempDir(t)
			reqDir := dir
			if tc.cd {
				reqDir = filepath.Join(dir, "elsewhere")
				if err := os.Mkdir(reqDir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			writeFile(t, reqDir, "sent body.md", tc.sent)
			for _, key := range []string{"42", "branch", "current"} {
				gh.Write(t, "pr_example_widget_"+key+".json", map[string]string{"body": tc.current})
			}
			data := testPayload(t, dir, tc.command, tc.response)
			out := hooktest.Output(t, Definition(), data, hooktest.Options{})
			calls := gh.Calls(t)
			args := append([]string{"pr", "view"}, tc.args...)
			args = append(args, "--json", "body", "--jq", ".body")
			if len(calls) != 1 || !slices.Equal(calls[0].Argv, args) || calls[0].Cwd != reqDir || calls[0].Repo != tc.repo {
				t.Fatalf("gh calls = %+v, want %v in %s with GH_REPO=%s", calls, args, reqDir, tc.repo)
			}
			if tc.name == "equivalent" {
				if out != "" {
					t.Errorf("equivalent text emitted %q", out)
				}
				return
			}
			got := hooktest.ParseInjection(t, out)
			diff := "--- GitHub\n+++ --body-file\n@@ -1,3 +1,3 @@\n # Context\n \n-Published line\n+Expected line\n"
			want := messages.Text(hooktest.Language, idMismatch, map[string]any{"Diff": diff})
			if got.Event != event || got.SystemMessage != "" || got.Context != want {
				t.Errorf("injection = %+v, want %s", got, want)
			}
		})
	}
}

func TestSilentOnUnsupportedOrFailedCommands(t *testing.T) {
	response := map[string]any{"stdout": "rc=0\n", "interrupted": false}
	for _, tc := range []struct {
		name, command, config, mode string
		response                    any
	}{
		{name: "unrelated", command: "gh pr view 42"},
		{name: "not pr", command: "gh issue edit 42 -F 'sent body.md'"},
		{name: "not gh", command: "echo gh pr edit 42 -F 'sent body.md'"},
		{name: "stdin", command: "gh pr edit 42 --body-file -"},
		{name: "missing file", command: "gh pr edit 42 -Fmissing.md"},
		{name: "dynamic file", command: "gh pr edit 42 -F \"$filename\""},
		{name: "glob file", command: "gh pr edit 42 -F sent*.md"},
		{name: "two files", command: editCommand + " -F other.md"},
		{name: "two selectors", command: editCommand + " branch"},
		{name: "body supplied twice", command: editCommand + " --body text"},
		{name: "attachment alters body", command: editCommand + " --attach photo.png"},
		{name: "missing flag value", command: editCommand + " -R"},
		{name: "alternate environment", command: "GH_HOST=other.example " + editCommand},
		{name: "bad syntax", command: editCommand + " '"},
		{name: "list", command: editCommand + "; true"},
		{name: "and list", command: "true && " + editCommand},
		{name: "optional command", command: editCommand + " || true"},
		{name: "pipeline", command: editCommand + " | cat"},
		{name: "input redirection", command: editCommand + " < other.md"},
		{name: "background", command: editCommand + " &"},
		{name: "dynamic directory", command: "cd \"$target\" && " + editCommand},
		{name: "directory from CDPATH", command: "cd elsewhere && " + editCommand},
		{name: "logical parent", command: "cd ./symlink/.. && " + editCommand},
		{name: "nonzero", command: editCommand, response: map[string]any{"exitCode": 7}},
		{name: "interrupted", command: editCommand, response: map[string]any{"interrupted": true}},
		{name: "failed status", command: editCommand + "; echo rc=$?", response: map[string]any{"stdout": "rc=1\n"}},
		{name: "missing status", command: editCommand + "; echo rc=$?", response: map[string]any{}},
		{name: "bad response shape", command: editCommand, response: []int{0}},
		{name: "create without stdout URL", command: "gh pr create -F'sent body.md'", response: map[string]any{"stderr": prURL}},
		{
			name: "combined output cannot override stdout", command: "gh pr create -F'sent body.md'",
			response: map[string]any{"stdout": "", "output": prURL},
		},
		{name: "create with ambiguous output", command: "gh pr create -F'sent body.md'", response: prURL + "\n" + prURL + "0"},
		{name: "create with invalid output", command: "gh pr create -F'sent body.md'", response: "https://github.com/example/widget/issues/42"},
		{name: "failure from gh view", command: editCommand, mode: "fail"},
		{name: "timeout from gh view", command: editCommand, mode: "hang"},
		{name: "disabled", command: editCommand, config: "hooks: {pr-body-roundtrip: {enabled: false}}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gh := hooktest.NewFakeGH(t)
			dir := hooktest.TempDir(t)
			writeFile(t, dir, "sent body.md", sentBody)
			gh.Write(t, "pr_example_widget_42.json", map[string]string{"body": current})
			if tc.mode != "" {
				gh.SetMode(t, tc.mode)
			}
			if tc.mode == "hang" {
				previous := fetchTimeout
				fetchTimeout = 150 * time.Millisecond
				t.Cleanup(func() { fetchTimeout = previous })
			}
			if tc.response == nil {
				tc.response = response
			}
			data := testPayload(t, dir, tc.command, tc.response)
			options := hooktest.Options{Config: tc.config}
			out := hooktest.Output(t, Definition(), data, options)
			if out != "" {
				t.Errorf("output = %q, want silence", out)
			}
			if tc.mode == "" {
				if calls := gh.Calls(t); len(calls) != 0 {
					t.Errorf("unexpected calls: %+v", calls)
				}
			}
		})
	}
}

func TestIgnoredPayloads(t *testing.T) {
	gh := hooktest.NewFakeGH(t)
	dir := hooktest.TempDir(t)
	data := testPayload(t, dir, editCommand, map[string]any{})
	for _, raw := range []string{
		"", "null", "[]", data + "{}", strings.Replace(data, event, "PreToolUse", 1),
		strings.Replace(data, "Bash", "REPL", 1),
		strings.Replace(data, `"tool_response":{}`, `"tool_response":null`, 1),
		`{"tool_input":{"command":["gh", "pr", "--body-file"]}}`,
	} {
		if out := hooktest.Output(t, Definition(), raw, hooktest.Options{}); out != "" {
			t.Errorf("payload %q emitted %q", raw, out)
		}
	}
	if calls := gh.Calls(t); len(calls) != 0 {
		t.Fatalf("unexpected calls %+v", calls)
	}
}

func TestDiffPreview(t *testing.T) {
	lines := strings.Repeat("match\n", 134)
	for _, tc := range []struct {
		old, next, excerpt string
		shortened          bool
	}{
		{"", "first", "@@ -0,0 +1 @@\n+first\n", false},
		{"first", "", "@@ -1 +0,0 @@\n-first\n", false},
		{lines + "first", lines + "edited", "-first\n+edited\n", false},
		{"first\n" + lines + "second", "edited\n" + lines + "another", "-first\n+edited\n", true},
		{strings.Repeat("a", maxLineBytes-1) + "あ", "updated", "…\n+updated\n", true},
		{strings.Repeat("old\n", 35), strings.Repeat("new\n", 35), "-old\n", true},
	} {
		got, shortened := diffPreview(tc.old, tc.next)
		if !strings.Contains(got, tc.excerpt) || shortened != tc.shortened || !utf8.ValidString(got) ||
			strings.Count(got, "\n") > previewLines+3 {
			t.Errorf("invalid diff: %q (shortened = %t), want %q (shortened = %t)",
				got, shortened, tc.excerpt, tc.shortened)
		}
	}
}
