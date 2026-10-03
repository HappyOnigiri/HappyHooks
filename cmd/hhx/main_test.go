package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/HappyOnigiri/hhx/internal/hookrt"
	"github.com/HappyOnigiri/hhx/internal/i18n"
	"github.com/HappyOnigiri/hhx/internal/registry"
)

func runCommand(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// isolate は HOME・PATH・設定ファイルを一時ディレクトリへ向ける。install のテストが実機の設定に触れないためである。
func isolate(t *testing.T) (home, binary string) {
	t.Helper()
	home = t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	binary = filepath.Join(bin, "hhx")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	t.Setenv("HHX_CONFIG", "")
	return home, binary
}

func TestUnknownHookIsSilentSuccess(t *testing.T) {
	isolate(t)
	code, stdout, stderr := runCommand(t, `{"tool_name":"Bash","tool_input":{"command":"gh pr merge 1"}}`, "hook", "no-such-hook")
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestVersionAndUsage(t *testing.T) {
	if code, stdout, _ := runCommand(t, "", "version"); code != 0 || !strings.HasPrefix(stdout, "hhx version ") {
		t.Fatalf("version: code=%d stdout=%q", code, stdout)
	}
	if code, _, _ := runCommand(t, ""); code != 2 {
		t.Fatalf("no arguments must exit 2, got %d", code)
	}
	if code, _, _ := runCommand(t, "", "bogus"); code != 2 {
		t.Fatalf("an unknown command must exit 2, got %d", code)
	}
}

func TestInstallAndUninstallRegisteredHooks(t *testing.T) {
	home, binary := isolate(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "{\n  \"model\": \"opus\"\n}\n"
	if err := os.WriteFile(settings, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ command, state string }{
		{"install", "updated"}, {"install", "unchanged"}, {"uninstall", "updated"}, {"uninstall", "unchanged"},
	} {
		code, stdout, stderr := runCommand(t, "", step.command)
		if code != 0 || !strings.Contains(stdout, "claude: "+settings+" ("+step.state+")") {
			t.Fatalf("%s: code=%d stdout=%q stderr=%q", step.command, code, stdout, stderr)
		}
		if strings.Contains(stdout, "codex") {
			t.Fatalf("%s must skip agents without a config directory: %q", step.command, stdout)
		}
		if step.command != "install" {
			continue
		}
		data, err := os.ReadFile(settings)
		if err != nil {
			t.Fatal(err)
		}
		for _, definition := range registry.All() {
			claude := false
			for _, registration := range definition.Registrations {
				claude = claude || registration.Agent == hookrt.Claude
			}
			if registered := strings.Contains(string(data), binary+" hook "+definition.Name); registered != claude {
				t.Fatalf("install registered %s=%v, want %v: %s", definition.Name, registered, claude, data)
			}
		}
	}
	data, err := os.ReadFile(settings)
	if err != nil || strings.Contains(string(data), " hook ") || !strings.Contains(string(data), `"model": "opus"`) {
		t.Fatalf("uninstall must remove only hhx entries: %q", data)
	}
}

// hookLayout は設定ファイルの hooks を「イベント → グループ（matcher: エントリ, ...）」の読みやすい形にする。
func hookLayout(t *testing.T, path, binary string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Hooks map[string][]struct {
			Matcher *string          `json:"matcher"`
			Hooks   []map[string]any `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	events := make([]string, 0, len(document.Hooks))
	for event := range document.Hooks {
		events = append(events, event)
	}
	sort.Strings(events)
	var lines []string
	for _, event := range events {
		for _, group := range document.Hooks[event] {
			matcher := "(none)"
			if group.Matcher != nil {
				matcher = *group.Matcher
			}
			var entries []string
			for _, entry := range group.Hooks {
				command, _ := entry["command"].(string)
				text := strings.TrimPrefix(command, binary+" hook ")
				for _, key := range []string{"timeout", "statusMessage", "additionalContextLimit"} {
					if value, ok := entry[key]; ok {
						text += fmt.Sprintf(" %s=%v", key, value)
					}
				}
				entries = append(entries, text)
			}
			lines = append(lines, event+" ["+matcher+"] "+strings.Join(entries, ", "))
		}
	}
	return strings.Join(lines, "\n")
}

// TestInstallWritesTheMigratedRegistrations は、install が移行元（Python 実装）と同じイベント・matcher・付加項目で
// 登録することを確かめる。移行元の登録から、hhx に入れない hook（wx へ移すものと削除するもの）を除いた形である。
func TestInstallWritesTheMigratedRegistrations(t *testing.T) {
	home, binary := isolate(t)
	for _, dir := range []string{".claude", ".codex"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if code, stdout, stderr := runCommand(t, "", "install"); code != 0 {
		t.Fatalf("install: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	claude := strings.Join([]string{
		"PostToolUse [Bash] push-ci-context timeout=10, pr-body-staleness timeout=15 statusMessage=Checking PR body freshness..., " +
			"generated-edit-guard",
		"PreToolUse [Bash] pr-merge-guard, discard-guard, git-hookspath-guard, irreversible-guard, dangerous-rm-guard, " +
			"forbidden-term-guard, idle-wait-guard, generated-edit-guard",
		"PreToolUse [Edit|Write|MultiEdit|NotebookEdit] git-hookspath-guard, irreversible-guard, generated-edit-guard",
		"PreToolUse [ExitPlanMode] exit-plan-subagent-guard",
		"UserPromptSubmit [(none)] pr-context timeout=15 statusMessage=Fetching PR context...",
	}, "\n")
	if got := hookLayout(t, filepath.Join(home, ".claude", "settings.json"), binary); got != claude {
		t.Errorf("claude:\n%s\nwant\n%s", got, claude)
	}
	codex := strings.Join([]string{
		"PostToolUse [Bash] push-ci-context timeout=10 additionalContextLimit=4096, " +
			"pr-body-staleness timeout=15 statusMessage=Checking PR body freshness... additionalContextLimit=4096, " +
			"generated-edit-guard additionalContextLimit=4096",
		"PreToolUse [(none)] agents-local-context timeout=10 additionalContextLimit=32768",
		"PreToolUse [Bash] pr-merge-guard, discard-guard, git-hookspath-guard, irreversible-guard, forbidden-term-guard, idle-wait-guard, " +
			"generated-edit-guard additionalContextLimit=4096",
		"PreToolUse [^(apply_patch|Edit|Write)$] git-hookspath-guard, irreversible-guard, " +
			"generated-edit-guard additionalContextLimit=4096",
		"SessionStart [(none)] agents-local-context timeout=10 additionalContextLimit=32768",
		"SessionStart [^compact$] agents-local-context timeout=10 additionalContextLimit=32768",
		"SubagentStart [(none)] agents-local-context timeout=10 additionalContextLimit=32768",
		"UserPromptSubmit [(none)] pr-context timeout=15 statusMessage=Fetching PR context...",
	}, "\n")
	if got := hookLayout(t, filepath.Join(home, ".codex", "hooks.json"), binary); got != codex {
		t.Errorf("codex:\n%s\nwant\n%s", got, codex)
	}
}

// `hhx hook <name>` が登録表の hook へ振り分けられ、stdin と argv の両方の経路で判定することを確かめる。
func TestHookDispatchesToRegisteredHook(t *testing.T) {
	isolate(t)
	t.Setenv("AGENT_ALLOW_PR_MERGE", "")
	payload := `{"tool_name":"Bash","tool_input":{"command":"gh pr merge 1"}}`
	code, stdout, stderr := runCommand(t, payload, "hook", "pr-merge-guard")
	if code != 0 || !strings.Contains(stdout, `"permissionDecision":"deny"`) || stderr != "" {
		t.Fatalf("stdin: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, _ = runCommand(t, "", "hook", "idle-wait-guard", "echo ok")
	if code != 0 || !strings.Contains(stdout, `"permissionDecision":"deny"`) {
		t.Fatalf("argv: code=%d stdout=%q", code, stdout)
	}
	// git-hookspath-guard は既定で無効である。
	code, stdout, _ = runCommand(t, "", "hook", "git-hookspath-guard", "git config core.hooksPath x")
	if code != 0 || stdout != "" {
		t.Fatalf("default-off hook: code=%d stdout=%q", code, stdout)
	}
}

// TestAllowGeneratedEdit は宣言のサブコマンドの形の検査と出力を確かめる。記録は hook が行うので、CLI はファイルに書かない。
func TestAllowGeneratedEdit(t *testing.T) {
	home, _ := isolate(t)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	code, stdout, stderr := runCommand(t, "", "allow-generated-edit", "--reason", "the user asked", "gen.go", "b.go")
	want := messages.Text(i18n.English, idAllowDeclared, map[string]any{"Paths": "gen.go, b.go"})
	if code != 0 || stdout != want || stderr != "" {
		t.Fatalf("declare: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(home, "cache")); !os.IsNotExist(err) {
		t.Errorf("the CLI must not record the declaration: %v", err)
	}
	for _, testCase := range []struct {
		args []string
		id   string
	}{
		{[]string{"gen.go"}, idAllowNoReason},
		{[]string{"--reason=", "gen.go"}, idAllowNoReason},
		{[]string{"--reason", "x"}, idAllowNoPaths},
		{[]string{"--force", "--reason", "x", "gen.go"}, idAllowUnknownOption},
	} {
		code, stdout, stderr := runCommand(t, "", append([]string{"allow-generated-edit"}, testCase.args...)...)
		want := messages.Text(i18n.English, testCase.id, map[string]any{"Option": "--force"})
		if code != 2 || stdout != "" || !strings.HasPrefix(stderr, want+"\n\n") ||
			!strings.HasSuffix(stderr, messages.T(i18n.English, idAllowUsage)) {
			t.Errorf("%q: code=%d stdout=%q stderr=%q", testCase.args, code, stdout, stderr)
		}
	}
	if code, stdout, _ := runCommand(t, "", "allow-generated-edit", "--help"); code != 0 ||
		stdout != messages.T(i18n.English, idAllowUsage) {
		t.Errorf("help: code=%d stdout=%q", code, stdout)
	}
	config := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(config, []byte("hooks:\n  generated-edit-guard:\n    enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HHX_CONFIG", config)
	code, stdout, _ = runCommand(t, "", "allow-generated-edit", "--reason", "x", "gen.go")
	if code != 0 || stdout != messages.T(i18n.English, idAllowDisabled) {
		t.Errorf("disabled: code=%d stdout=%q", code, stdout)
	}
}

func TestInstallNeedsAnAgent(t *testing.T) {
	isolate(t)
	if code, _, stderr := runCommand(t, "", "install"); code != 1 || !strings.Contains(stderr, "--agent") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if code, _, _ := runCommand(t, "", "install", "--agent", "cursor"); code != 2 {
		t.Fatalf("an unknown agent must exit 2, got %d", code)
	}
}

func TestInstallRejectsBrokenConfig(t *testing.T) {
	home, _ := isolate(t)
	path := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(path, []byte("hooks:\n  no-such-hook:\n    enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HHX_CONFIG", path)
	code, _, stderr := runCommand(t, "", "install", "--agent", "claude")
	if code != 1 || !strings.Contains(stderr, "unknown hooks: no-such-hook") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	// 実行時の hook は壊れた設定でも止まらない。
	if code, stdout, _ := runCommand(t, "{}", "hook", "no-such-hook"); code != 0 || stdout != "" {
		t.Fatalf("hook: code=%d stdout=%q", code, stdout)
	}
}

func TestValidateConfigReportsHookSettingTypes(t *testing.T) {
	home, _ := isolate(t)
	path := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(path, []byte("hooks:\n  limited:\n    limit: abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HHX_CONFIG", path)
	type settings struct {
		Limit int `yaml:"limit"`
	}
	definitions := []hookrt.Definition{{Name: "limited", NewSettings: func() any { return &settings{} }}}
	if err := validateConfig(definitions); err == nil || !strings.Contains(err.Error(), "hooks.limited") {
		t.Fatalf("validateConfig()=%v, want an error for hooks.limited", err)
	}
	definitions[0].NewSettings = nil
	if err := validateConfig(definitions); err != nil {
		t.Fatalf("hook without settings: validateConfig()=%v", err)
	}
}

func TestInstallRequiresHHXOnPath(t *testing.T) {
	home, binary := isolate(t)
	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCommand(t, "", "install"); code != 1 || !strings.Contains(stderr, "not on PATH") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	// uninstall は hhx が PATH から消えた後でも使える。
	if code, _, stderr := runCommand(t, "", "uninstall"); code != 0 {
		t.Fatalf("uninstall: code=%d stderr=%q", code, stderr)
	}
}
