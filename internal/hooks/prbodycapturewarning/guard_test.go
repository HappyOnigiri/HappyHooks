package prbodycapturewarning

import (
	"strings"
	"testing"

	"github.com/HappyOnigiri/hhx/internal/hooktest"
	"github.com/HappyOnigiri/hhx/internal/i18n"
)

func TestBash(t *testing.T) {
	const view = `gh pr view 12 --json body -q '.body'`
	for _, tc := range []struct {
		name, command string
		warn          bool
	}{
		{"capture", `body=$(` + view + ` 2>&1)`, true},
		{"file", view + ` > body.md 2>&1`, true},
		{"file and diagnostics", view + ` &> body.md`, true},
		{"file and diagnostics shorthand", view + ` >& body.md`, true},
		{"append both", view + ` &>> body.md`, true},
		{"append streams to the same file", view + ` >> body.md 2>> 'body.md'`, true},
		{"append streams to different files", view + ` >> body.md 2>> diagnostics.log`, false},
		{"overridden same file", view + ` > body.md 2> body.md > other.md`, false},
		{"same file inherited by a group", `{ ` + view + ` 2>> body.md; } >> body.md`, true},
		{"brace group", `{ ` + view + `; } > body.md 2>&1`, true},
		{"nested subshell", `( { ` + view + `; } ) &> body.md`, true},
		{"group child stderr redirected", `{ ` + view + ` 2> diagnostics.log; } &> body.md`, false},
		{"group separate statements", `{ printf '%s' status; } &> body.md; ` + view, false},
		{"group command substitution", `{ body=$(` + view + `); } &> body.md`, false},
		{"pipeline", view + ` 2>&1 | tee body.md`, true},
		{"pipeline shorthand", view + ` |& tee body.md`, true},
		{"middle of a pipeline", `printf '%s' input | ` + view + ` |& tee body.md`, true},
		{"first stage merges in a long pipeline", view + ` |& cat | tee body.md`, true},
		{"unrelated pipeline stage merges", view + ` | cat |& tee body.md`, false},
		{"group pipes both streams", `{ ` + view + `; } |& tee body.md`, true},
		{"pipe-all overrides stderr redirect", view + ` 2> diagnostics.log |& tee body.md`, true},
		{"duplicate to stderr", view + ` >&2`, true},
		{"multiple statements", `git status` + "\n" + view + ` > body.md 2>&1`, true},
		{"fields and option values", `/usr/bin/gh pr view "$pr" --json=title,body --jq=.body > "$tmp" 2>&1`, true},
		{"REST", `gh api repos/example/widget/pulls/12 --jq .body > body.md 2>&1`, true},
		{"REST method first", `gh api --method GET repos/example/widget/pulls/12 --jq .body > body.md 2>&1`, true},
		{"stdout file", view + ` > body.md`, false},
		{"stdout variable", `body=$(` + view + `)`, false},
		{"stdout pipe", view + ` | tee body.md`, false},
		{"split files", view + ` > body.md 2> diagnostics.log`, false},
		{"redirect order", view + ` 2>&1 > body.md`, false},
		{"stderr reassigned", view + ` &> body.md 2>/dev/null`, false},
		{"view", `gh pr view 12`, false},
		{"title only", `gh pr view 12 --json body,title -q .title 2>&1`, false},
		{"REST comments", `gh api repos/example/widget/pulls/12/comments --jq .body 2>&1`, false},
		{"REST update response", `gh api repos/example/widget/pulls/12 -X PATCH -f body=updated --jq .body 2>&1`, false},
		{"different command merges", view + `; cat body.md 2>&1`, false},
		{"quoted example", `printf '%s\n' 'gh pr view 12 --json body -q .body 2>&1'`, false},
		{"quoted argument", view + ` '2>&1'`, false},
		{"malformed", view + ` 2>&1 |`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := ""
			if tc.warn {
				id = idMixedStreams
			}
			checkWarning(t, hooktest.BashPayload(tc.command, "", nil), id, hooktest.Options{})
		})
	}
}

func TestREPL(t *testing.T) {
	const view = "gh pr view ${pr} --json body -q .body"
	for _, tc := range []struct {
		name, code string
		warn       bool
	}{
		{"observed capture", "const cur = await $`gh pr view 12 --json body -q .body`.text();\n" +
			"await write(sp+'/body.md', cur.replace(oldLine, newLine));", true},
		{"observed cross-repository capture", "const cur = await $`gh pr view 12 --repo example/widget --json body -q .body`.text();", true},
		{"parenthesized", "const body = (await $`" + view + "`).text()", true},
		{"REST capture", "const body = await $`gh api repos/example/widget/pulls/12 --jq '.body'`.nothrow().text()", true},
		{"stderr redirected in command", "const body = await $`" + view + " 2>/dev/null`.text()", true},
		{"piped stdout in capture", "const body = await $`" + view + " | cat`.text()", true},
		{"multiple commands", "await $`gh pr view 12`; const body = await $`" + view + "`.text()", true},
		{"file", "await $`" + view + " > ${file}`; const body = await read(file)", false},
		{"file with captured status", "await $`" + view + " > ${file}`.text()", false},
		{"explicitly merged file", "await $`" + view + " > ${file} 2>&1`.text()", true},
		{"view", "await $`gh pr view 12`.text()", false},
		{"display only", "await $`" + view + "`", false},
		{"shell example in a string", "const example = \"try $`" + view + "`.text()\"", false},
		{"shell example in a comment", "// try $`" + view + "`.text()", false},
		{"different template tag", "const body = await example$`" + view + "`.text()", false},
		{"capture after a shell example", "const example = \"try $`" + view + "`.text()\"; await $`" + view + "`.text()", true},
		{"other command capture", "await $`" + view + " > ${file}`; await $`git status`.text()", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := ""
			if tc.warn {
				id = idREPLCapture
			}
			checkWarning(t, hooktest.ToolPayload("REPL", map[string]any{"code": tc.code}, ""), id, hooktest.Options{})
		})
	}
	checkWarning(t, hooktest.ToolPayload("REPL", map[string]any{
		"code": "await $`" + view + " > ${file} 2>&1`",
	}, ""), idMixedStreams, hooktest.Options{})
}

func TestPayloadAndConfiguration(t *testing.T) {
	command := `gh pr view 12 --json body 2>&1`
	for _, raw := range []string{
		"", "null", "[]", `{"gh":"body &"`,
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":"gh body &"}`,
		`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"gh pr view --json body 2>&1"}}`,
		hooktest.ToolPayload("Bash", map[string]any{"command": 1, "description": command}, ""),
		hooktest.ToolPayload("Other", map[string]any{"command": command}, ""),
		hooktest.BashPayload(strings.Repeat(" ", maxCodeBytes+1)+command, "", nil),
		hooktest.ToolPayload("REPL", map[string]any{"code": strings.Repeat(" ", maxCodeBytes+1) + "await $`" + command + "`.text()"}, ""),
	} {
		checkWarning(t, raw, "", hooktest.Options{})
	}
	checkWarning(t, hooktest.BashPayload(command, "", nil), "", hooktest.Options{
		Config: "hooks:\n  pr-body-capture-warning:\n    enabled: false\n",
	})
	checkWarning(t, "", idMixedStreams, hooktest.Options{Args: []string{hooktest.BashPayload(command, "", nil)}})
}

func checkWarning(t *testing.T, raw, id string, options hooktest.Options) {
	t.Helper()
	output := hooktest.Output(t, Definition(), raw, options)
	got := hooktest.ParseInjection(t, output)
	want := hooktest.Injection{}
	if id != "" {
		want.Event = event
		want.Context = messages.T(hooktest.Language, id) + "\n" + messages.T(hooktest.Language, idAction)
	}
	if got != want {
		t.Errorf("warning = %#v, want %#v", got, want)
	}
}

func TestContextLimit(t *testing.T) {
	for _, language := range []i18n.Language{i18n.English, i18n.Japanese} {
		for _, id := range []string{idMixedStreams, idREPLCapture} {
			if size := len(messages.T(language, id) + "\n" + messages.T(language, idAction)); size > contextLimit {
				t.Errorf("%s %s warning uses %d bytes, limit %d", language, id, size, contextLimit)
			}
		}
	}
}
