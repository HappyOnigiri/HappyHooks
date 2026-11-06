package blockingcommandguard

import (
	"strings"
	"testing"

	"github.com/HappyOnigiri/hhx/internal/hookrt"
	"github.com/HappyOnigiri/hhx/internal/hooktest"
	"github.com/HappyOnigiri/hhx/internal/pycompat"
)

func TestStdinReaders(t *testing.T) {
	commands := []string{
		"cat", "cat -", "cat -- -", "cat -n", "cat -bn", "/bin/cat", "/usr/bin/cat", "'cat'", `"cat"`,
		"cat > /tmp/output 2>/dev/null", "cat >> /tmp/output", "cat /tmp/input -", "tee", "tee /tmp/output", "tee -a /tmp/output",
		"sort", "sort -nr", "wc -l", "wc --", "head", "head -n 2", "head -n2", "tail -c 2", "tail -q",
		"grep needle", "grep -F needle", "grep -n needle -", "sed p", "sed -n p", "sed -n p -",
		"python3", "python3 -", "python -I -", "python3 -Bu", "sh", "sh -s", "bash -s argument", "zsh --",
		"node", "node -", "ruby", "perl -", "read", "read -r line", "read _value value2",
		"printf start; cat; printf end", "cd /tmp && cat > /tmp/output 2>/dev/null; python3 - <<'EOF'\nprint('ok')\nEOF",
		"{ cat; }", "(cat)", "false || cat", "true && cat", "! cat", "cat | wc -l", "cat |& wc -l",
		"exec > /tmp/output; cat", "(exec </dev/null); cat", "{ cat; } > /tmp/output", "cat 2</dev/null",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			got := hooktest.Stdin(t, Definition(), hooktest.BashPayload(command, "/tmp", nil))
			want := messages.Text(hooktest.Language, idReason, map[string]any{
				"Label": "STDIN", "Command": pycompat.QuoteJSON(command),
			})
			if got.Decision != hooktest.Deny || got.Reason != want {
				t.Fatalf("got %+v, want STDIN deny with catalog reason", got)
			}
		})
	}
}

func TestExplicitModes(t *testing.T) {
	cases := map[string]string{
		"tail -f /tmp/log": "FOLLOW", "tail -F /tmp/log": "FOLLOW", "tail --follow=name /tmp/log": "FOLLOW",
		"watch date": "FOLLOW", "yes": "STREAM", "yes answer": "STREAM",
		"vim": "EDITOR", "vi /tmp/file": "EDITOR", "nvim /tmp/file": "EDITOR", "nano": "EDITOR", "emacs": "EDITOR",
		"top": "INTERACTIVE", "htop": "INTERACTIVE",
		"fzf": "INTERACTIVE", "node -i": "INTERACTIVE", "node --interactive": "INTERACTIVE",
		"bash -i": "INTERACTIVE", "python3 -i": "INTERACTIVE",
		"git add -p": "INTERACTIVE", "git add --interactive": "INTERACTIVE", "git add -v --patch -- file": "INTERACTIVE",
		"git rebase -i HEAD~2": "INTERACTIVE", "git -C /tmp/repo rebase --interactive --root": "INTERACTIVE",
		"git commit --edit": "INTERACTIVE", "git commit -m message -e": "INTERACTIVE",
		"git commit --message=message --edit": "INTERACTIVE", "git rebase --exec command -i HEAD~2": "INTERACTIVE",
	}
	for command, label := range cases {
		t.Run(command, func(t *testing.T) {
			if got := classify(command); got != label {
				t.Fatalf("got %q, want %q", got, label)
			}
			if got := hooktest.Argv(t, Definition(), command); got.Decision != hooktest.Deny {
				t.Fatalf("argv: %+v", got)
			}
		})
	}
}

func TestExplicitInputAndFiniteCommandsPass(t *testing.T) {
	commands := []string{
		"cat /tmp/input", "cat </tmp/input", "cat </dev/null", "cat <&0", "cat <&3", "cat <&-", "cat 0>/tmp/output",
		"cat <<'EOF'\ncat\nEOF", "cat <<-EOF\n\tcat\nEOF", "cat <<<'data'", "printf data | cat", "printf data |& cat",
		"{ cat; } </tmp/input", "(cat) </tmp/input", "{ cat </tmp/input; }; cat /tmp/other",
		"exec </tmp/input; cat", "exec </tmp/input && cat", "exec </dev/null; python3 - <<'PY'\nprint('ok')\nPY",
		"python3 -c 'print(1)'", "python3 -m unittest", "python3 /tmp/script.py", "sh /tmp/script.sh", "bash -c 'cat'",
		"python3 - <<'EOF'\nprint('cat')\nEOF", "printf '{}' | python3 -c 'import json,sys; print(json.load(sys.stdin))'",
		"gh pr create --body-file - <<'EOF'\nbody\nEOF", "while IFS= read -r line; do printf '%s' \"$line\"; done < /tmp/input",
		"wc /tmp/input", "sort /tmp/input", "head -n 2 /tmp/input", "head -n0", "head -n 0", "read -t 1 line", "read -u 3 line",
		"vim -es /tmp/file", "nvim --headless", "emacs --batch", "less -F /tmp/file", "top -l 1", "fzf --filter pattern",
		"tail -f /tmp/log &", "tail -f /tmp/log | head -n 1", "watch date &", "yes | head -n 1", "printf data | less",
		"git log", "git diff", "git commit -m message", "git commit --amend --no-edit", "git add -- file",
		"git add -p --dry-run", "git add -p -n", "git add -- -p", "git rebase --interactive --abort",
		"git commit -m --edit", "git commit --message --edit", "git rebase --exec --interactive HEAD~2",
		"git -c core.editor=true commit --edit", "git -C", "git", "git status -i", "git rebase -i --unknown HEAD~2",
		"go test ./...", "go build ./...", "make build", "hhx wait-ci 1", "sleep 120; go test ./...",
	}
	checkPasses(t, commands)
}

func TestUncertainAndInvalidCommandsPass(t *testing.T) {
	commands := []string{
		"", "# cat", "printf cat", "printf 'cat; read'", "cat 'unterminated", "cat $FILES", "cat *.txt", `cat "${FILE}"`,
		"cat $(printf /tmp/input)", "cat <(printf data)", "cat ~/file", "cat $'data'", "cat --help", "wc --version", "tee --help",
		"cat --unknown", "head -n", "head -n invalid", "head -n -1", "head -n 2k", "head -n ''", "sort -o /tmp/output",
		"grep", "grep '['", "grep -e pattern", "grep -f /tmp/patterns", "grep pattern /tmp/input", "sed q", "sed -e p", "awk 'BEGIN {exit}'",
		"python3 --version", "python3 -- /tmp/script", "node --help", "sh -c", "sh -s -c true", "bash -s --help",
		"read -p prompt", "read a-b", "read ''", "less /tmp/file", "more /tmp/file",
		"cat(){ printf data; }; cat", "alias cat=printf; cat", "source /tmp/env; cat", ". /tmp/env; cat", "eval command; cat",
		"trap command EXIT; cat", "GIT_EDITOR=true git commit --edit", "FILES=/tmp/input; cat", "exec true; cat", "exit; cat",
		"return; cat", "if test -f /tmp/input; then cat; fi", "while read line; do cat; done", "for item in a; do cat; done",
		"export GIT_EDITOR=true; git commit --edit", "command exec </tmp/input; cat", "set -e; false; cat",
		"! true && cat", "! false || cat", `"$command" </tmp/input; cat`, "top /tmp/file", "fzf /tmp/file",
		"case value in value) cat;; esac", "if test -f /tmp/input; then exec </tmp/input; fi; cat", "false && cat", "true || cat",
		"tail -f", "tail --follow /tmp/log --pid=1", "tail -n 1 -f /tmp/log", "tail --help", "watch", "watch -g date",
		"yes --help", "node -i --version", "less -X /tmp/file", "/tmp/cat", "./cat", "git -C /tmp/repo", "tail -f ''",
	}
	checkPasses(t, commands)
	if got := classify(strings.Repeat("cat ", 128<<10)); got != "" {
		t.Fatalf("oversized command: %q", got)
	}
}

func checkPasses(t *testing.T, commands []string) {
	t.Helper()
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			got := hooktest.Stdin(t, Definition(), hooktest.BashPayload(command, "/tmp", nil))
			if got.Decision != "" || got.Reason != "" {
				t.Fatalf("must pass: %+v", got)
			}
		})
	}
}

func TestPayloadAndConfiguration(t *testing.T) {
	for _, payload := range []string{
		"", "null", "[]", `{"tool_input":null}`, `{"tool_input":[]}`, `{"tool_input":{"command":null}}`,
		`{"tool_input":{"command":1}}`, `{"tool_input":{"command":[]}}`, `{"tool_input":{"command":"cat","run_in_background":"true"}}`,
	} {
		if got := hooktest.Stdin(t, Definition(), payload); got.Decision != "" {
			t.Fatalf("payload %s: %+v", payload, got)
		}
	}
	for _, options := range []hooktest.Options{
		{Config: "hooks:\n  blocking-command-guard:\n    enabled: false\n"},
	} {
		if got := hooktest.Run(t, Definition(), hooktest.BashPayload("cat", "/tmp", nil), options); got.Decision != "" {
			t.Fatalf("disabled: %+v", got)
		}
	}
	if got := hooktest.Stdin(t, Definition(), hooktest.BashPayload("cat", "/tmp", map[string]any{"run_in_background": true})); got.Decision != "" {
		t.Fatalf("explicit background: %+v", got)
	}
	if got := hooktest.Argv(t, Definition(), "cat"); got.Decision != hooktest.Deny {
		t.Fatalf("argv: %+v", got)
	}
	if gate([]byte("make build")) || !gate([]byte("cat")) {
		t.Fatal("gate keywords must precede parsing")
	}
	if gate([]byte(hooktest.BashPayload("make build", "/tmp", nil))) {
		t.Fatal("the Bash tool name alone must not trigger the gate")
	}
	for _, command := range []string{"sh", "sh -s", "/bin/sh", "bash", "zsh"} {
		if !gate([]byte(hooktest.BashPayload(command, "/tmp", nil))) {
			t.Fatalf("shell gate missed %q", command)
		}
	}
}

func TestDefinition(t *testing.T) {
	d := Definition()
	if !d.DefaultEnabled || d.Name != Name || len(d.Registrations) != 2 {
		t.Fatalf("definition: %+v", d)
	}
	for _, r := range d.Registrations {
		if r.Event != "PreToolUse" || r.Matcher != "Bash" || r.Timeout != 0 || (r.Agent != hookrt.Claude && r.Agent != hookrt.Codex) {
			t.Fatalf("registration: %+v", r)
		}
	}
}
