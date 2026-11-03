package agentslocalcontext

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HappyOnigiri/hhx/internal/hooktest"
	"github.com/HappyOnigiri/hhx/internal/i18n"
)

func TestMain(m *testing.M) {
	os.Exit(hooktest.Main(m))
}

// fixture は Git のリポジトリ（ルートと src に AGENTS.local.md）と、テストごとの状態の置き場である。
type fixture struct {
	tmp, repo, nested, target, cache string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	tmp := hooktest.TempDir(t)
	f := &fixture{tmp: tmp, repo: filepath.Join(tmp, "repo")}
	hooktest.GitRepo(t, f.repo, "", false)
	hooktest.WriteFile(t, filepath.Join(f.repo, ruleName), "root-local\n")
	f.nested = filepath.Join(f.repo, "src", "service")
	hooktest.WriteFile(t, filepath.Join(f.repo, "src", ruleName), "src-local\n")
	f.target = filepath.Join(f.nested, "target.py")
	hooktest.WriteFile(t, f.target, "print('ok')\n")
	f.cache = filepath.Join(tmp, "cache")
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", f.cache)
	return f
}

type payloadOptions struct {
	event, session, toolName, source string
	toolInput                        any
	cwd                              string
}

func (f *fixture) payload(options payloadOptions) map[string]any {
	if options.event == "" {
		options.event = "PreToolUse"
	}
	if options.session == "" {
		options.session = "session-1"
	}
	if options.cwd == "" {
		options.cwd = f.repo
	}
	data := map[string]any{
		"session_id":      options.session,
		"transcript_path": filepath.Join(f.tmp, "transcript.jsonl"),
		"hook_event_name": options.event,
		"cwd":             options.cwd,
	}
	if options.event == "PreToolUse" {
		toolName := options.toolName
		if toolName == "" {
			toolName = "Bash"
		}
		toolInput := options.toolInput
		if toolInput == nil {
			toolInput = map[string]any{"command": "sed -n 1p " + f.target}
		}
		data["turn_id"], data["tool_name"], data["tool_use_id"], data["tool_input"] = "turn-1", toolName, "tool-1", toolInput
	}
	if options.source != "" {
		data["source"] = options.source
	}
	return data
}

func (f *fixture) run(t *testing.T, options payloadOptions) hooktest.Injection {
	t.Helper()
	raw, err := json.Marshal(f.payload(options))
	if err != nil {
		t.Fatal(err)
	}
	return runRaw(t, string(raw))
}

func runRaw(t *testing.T, raw string) hooktest.Injection {
	t.Helper()
	return hooktest.ParseInjection(t, hooktest.Output(t, Definition(), raw, hooktest.Options{}))
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

func TestInjectsRootToTargetRulesOncePerSession(t *testing.T) {
	f := newFixture(t)
	first := f.run(t, payloadOptions{})
	if first.Event != "PreToolUse" || first.SystemMessage != "" {
		t.Fatalf("injection: %+v", first)
	}
	want := "<agents-local-instructions>\nsource: " + f.repo + "/AGENTS.local.md\nscope: " + f.repo + "/**\n" +
		"<INSTRUCTIONS>\nroot-local\n</INSTRUCTIONS>\n</agents-local-instructions>\n\n" +
		"<agents-local-instructions>\nsource: " + f.repo + "/src/AGENTS.local.md\nscope: " + f.repo + "/src/**\n" +
		"<INSTRUCTIONS>\nsrc-local\n</INSTRUCTIONS>\n</agents-local-instructions>"
	if first.Context != want {
		t.Fatalf("context=\n%s\nwant\n%s", first.Context, want)
	}
	if again := f.run(t, payloadOptions{}); again != (hooktest.Injection{}) {
		t.Fatalf("the same session must not get the rules twice: %+v", again)
	}
	info, err := os.Stat(filepath.Join(f.cache, "hhx", Name))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("the state directory must be 0700: %v %v", info, err)
	}
	state := filepath.Join(f.cache, "hhx", Name, fileName("session-1"))
	if info, err := os.Stat(state); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the state file must be 0600: %v %v", info, err)
	}
}

func TestContentChangeIsReinjected(t *testing.T) {
	f := newFixture(t)
	f.run(t, payloadOptions{})
	hooktest.WriteFile(t, filepath.Join(f.repo, "src", ruleName), "src-local-v2\n")
	context := f.run(t, payloadOptions{}).Context
	mustContain(t, context, "src-local-v2")
	mustNotContain(t, context, "root-local")
}

func TestDifferentSessionGetsItsOwnContext(t *testing.T) {
	f := newFixture(t)
	if f.run(t, payloadOptions{}).Context == "" || f.run(t, payloadOptions{session: "session-2"}).Context == "" {
		t.Fatal("each session must get the rules")
	}
	// 安全な文字に絞ると同じになる session_id でも、別のセッションとして扱う。
	if f.run(t, payloadOptions{session: "session.2"}).Context == "" {
		t.Fatal("session ids that sanitize to the same name must not share the record")
	}
}

func TestToolInputsAddContextWithoutBlocking(t *testing.T) {
	f := newFixture(t)
	patch := "*** Begin Patch\n*** Update File: src/service/target.py\n@@\n-print('ok')\n+print('updated')\n*** End Patch"
	mustContain(t, f.run(t, payloadOptions{session: "patch", toolName: "apply_patch", toolInput: map[string]any{"command": patch}}).Context, "src-local")
	for _, tool := range []string{"Write", "Edit"} {
		injection := f.run(t, payloadOptions{session: tool, toolName: tool, toolInput: map[string]any{"file_path": f.target, "content": "updated"}})
		mustContain(t, injection.Context, "src-local")
	}
	mustContain(t, f.run(t, payloadOptions{session: "mcp", toolName: "mcp__filesystem__read_file", toolInput: map[string]any{"file_path": f.target}}).Context, "src-local")
	mustContain(t, f.run(t, payloadOptions{session: "string", toolName: "custom", toolInput: "cat " + f.target}).Context, "src-local")
	mustContain(t, f.run(t, payloadOptions{session: "workdir", toolInput: map[string]any{"command": "ls", "workdir": "src/service"}}).Context, "src-local")
	mustContain(t, f.run(t, payloadOptions{session: "nested", toolName: "x", toolInput: map[string]any{"args": []any{map[string]any{"filepath": f.target}}}}).Context, "src-local")
}

func TestCommandParseFailureIsSilentWithoutFallbackTokens(t *testing.T) {
	f := newFixture(t)
	command := "cat <<'PY'\ncd src/service\nconst n = /data-page=\"/g;\nPY"
	injection := f.run(t, payloadOptions{session: "parse-fallback-session", toolInput: map[string]any{"command": command}})
	if injection.SystemMessage != "" {
		t.Fatalf("a parse failure must not warn: %q", injection.SystemMessage)
	}
	mustContain(t, injection.Context, "root-local")
	mustNotContain(t, injection.Context, "src-local")
}

func TestNoRuleIsSilent(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(f.tmp, "outside")
	_ = os.Mkdir(outside, 0o755)
	output := hooktest.Output(t, Definition(), string(mustJSON(f.payload(payloadOptions{
		cwd:       outside,
		toolInput: map[string]any{"command": "ls " + outside},
	}))), hooktest.Options{})
	if output != "" {
		t.Fatalf("output: %q", output)
	}
}

func TestStateFailureWarnsAndStillInjects(t *testing.T) {
	f := newFixture(t)
	invalid := filepath.Join(f.tmp, "not-a-directory")
	hooktest.WriteFile(t, invalid, "file\n")
	t.Setenv("XDG_CACHE_HOME", invalid)
	injection := f.run(t, payloadOptions{})
	mustContain(t, injection.SystemMessage, warningPrefix, warningKind(idStateClaim))
	mustContain(t, injection.Context, "root-local")
	mustContain(t, f.run(t, payloadOptions{event: "SubagentStart"}).SystemMessage, warningKind(idStateStored))
	compact := f.run(t, payloadOptions{event: "SessionStart", source: "compact"})
	mustContain(t, compact.SystemMessage, warningKind(idStateStored), warningKind(idStateReplace))
}

func TestRuleReadFailureWarnsAndKeepsOtherContext(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.repo, "src", ruleName), []byte{0xff, 0xfe}, 0o644); err != nil {
		t.Fatal(err)
	}
	injection := f.run(t, payloadOptions{})
	mustContain(t, injection.SystemMessage, messages.Text(hooktest.Language, idReadRule, map[string]any{"Path": f.repo + "/src/AGENTS.local.md", "Error": ""}))
	mustContain(t, injection.Context, "root-local")
}

func TestInvalidInputWarns(t *testing.T) {
	newFixture(t)
	for raw, want := range map[string]string{
		"not-json":                          warningKind(idInvalidInput),
		"":                                  warningKind(idInvalidInput),
		"{} x":                              warningKind(idInvalidInput),
		"\xff":                              warningKind(idInvalidInput),
		"[]":                                warningKind(idNotObject),
		"null":                              warningKind(idNotObject),
		`{"hook_event_name": "PreToolUse"}`: warningKind(idNoCwd),
	} {
		injection := runRaw(t, raw)
		if injection.Event != "" || !strings.Contains(injection.SystemMessage, want) {
			t.Errorf("raw %q: %+v, want a warning with %q", raw, injection, want)
		}
	}
}

func TestMissingSessionIDDisablesDeduplication(t *testing.T) {
	f := newFixture(t)
	raw := string(mustJSON(map[string]any{"hook_event_name": "PreToolUse", "cwd": f.repo, "tool_input": map[string]any{"command": "cat " + f.target}}))
	for range 2 {
		injection := runRaw(t, raw)
		mustContain(t, injection.Context, "src-local")
		mustContain(t, injection.SystemMessage, warningKind(idNoSessionClaim))
	}
	subagent := runRaw(t, string(mustJSON(map[string]any{"hook_event_name": "SubagentStart", "cwd": f.repo})))
	mustContain(t, subagent.SystemMessage, warningKind(idNoSessionStored))
	// compact は記録を置き換えないので、復元できない警告だけを出す。
	compact := runRaw(t, string(mustJSON(map[string]any{"hook_event_name": "SessionStart", "source": "compact", "cwd": f.repo})))
	if compact.SystemMessage != warningPrefix+messages.T(hooktest.Language, idNoSessionStored) {
		t.Fatalf("compact: %+v", compact)
	}
}

func TestCompactReinjectsAllLoadedRules(t *testing.T) {
	f := newFixture(t)
	f.run(t, payloadOptions{})
	compact := f.run(t, payloadOptions{event: "SessionStart", source: "compact"})
	if compact.Event != "SessionStart" {
		t.Fatalf("compact: %+v", compact)
	}
	mustContain(t, compact.Context, "root-local", "src-local")
	if again := f.run(t, payloadOptions{}); again.Context != "" {
		t.Fatalf("the replaced record must still suppress the rules: %+v", again)
	}
	// 消えたルールは記録からも外れる。
	_ = os.Remove(filepath.Join(f.repo, "src", ruleName))
	f.run(t, payloadOptions{event: "SessionStart", source: "compact"})
	hooktest.WriteFile(t, filepath.Join(f.repo, "src", ruleName), "src-local\n")
	mustContain(t, f.run(t, payloadOptions{}).Context, "src-local")
}

func TestSessionStartInjectsCwdRulesWithoutRepeatingThemAtToolUse(t *testing.T) {
	f := newFixture(t)
	start := f.run(t, payloadOptions{event: "SessionStart", source: "startup"})
	if start.Event != "SessionStart" {
		t.Fatalf("start: %+v", start)
	}
	mustContain(t, start.Context, "root-local")
	mustNotContain(t, start.Context, "src-local")
	if again := f.run(t, payloadOptions{event: "SessionStart", source: "startup"}); again.Context != "" {
		t.Fatalf("again: %+v", again)
	}
	tool := f.run(t, payloadOptions{}).Context
	mustContain(t, tool, "src-local")
	mustNotContain(t, tool, "root-local")
}

func TestSubagentStartReinjectsAllLoadedRulesEachTime(t *testing.T) {
	f := newFixture(t)
	f.run(t, payloadOptions{})
	first := f.run(t, payloadOptions{event: "SubagentStart"})
	second := f.run(t, payloadOptions{event: "SubagentStart"})
	if first.Event != "SubagentStart" || first != second {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	mustContain(t, first.Context, "root-local", "src-local")
	if empty := f.run(t, payloadOptions{event: "SubagentStart", session: "fresh"}); empty != (hooktest.Injection{}) {
		t.Fatalf("a session without records must get nothing: %+v", empty)
	}
}

func TestOtherEventsKeepTheState(t *testing.T) {
	f := newFixture(t)
	f.run(t, payloadOptions{})
	for _, options := range []payloadOptions{{event: "SessionStart", source: "clear"}, {event: "SessionEnd"}, {event: "Stop"}} {
		if injection := f.run(t, options); options.source != "clear" && injection != (hooktest.Injection{}) {
			t.Errorf("%+v: %+v", options, injection)
		}
	}
	if again := f.run(t, payloadOptions{}); again.Context != "" {
		t.Fatalf("the state must survive: %+v", again)
	}
}

func TestParallelCallsEmitEachUnchangedRuleOnce(t *testing.T) {
	f := newFixture(t)
	raw := string(mustJSON(f.payload(payloadOptions{session: "parallel-session"})))
	outputs := make([]string, 8)
	var group sync.WaitGroup
	for index := range outputs {
		group.Go(func() { outputs[index] = hooktest.Output(t, Definition(), raw, hooktest.Options{}) })
	}
	group.Wait()
	var emitted []string
	for _, output := range outputs {
		if output != "" {
			emitted = append(emitted, output)
		}
	}
	if len(emitted) != 1 {
		t.Fatalf("exactly one call must inject, got %d: %q", len(emitted), emitted)
	}
	mustContain(t, emitted[0], "root-local", "src-local")
}

func TestExpiredEntriesAndSessionsArePruned(t *testing.T) {
	f := newFixture(t)
	f.run(t, payloadOptions{})
	dir := filepath.Join(f.cache, "hhx", Name)
	path := filepath.Join(dir, fileName("session-1"))
	old := time.Now().Add(-31 * 24 * time.Hour)
	state := stateFile{Rules: map[string]stateEntry{}}
	data, _ := os.ReadFile(path)
	_ = json.Unmarshal(data, &state)
	for key, entry := range state.Rules {
		entry.LoadedAt = old.Unix()
		state.Rules[key] = entry
	}
	data = mustJSON(state)
	_ = os.WriteFile(path, data, 0o600)
	stale := filepath.Join(dir, fileName("stale"))
	_ = os.WriteFile(stale, []byte("{}"), 0o600)
	_ = os.Chtimes(stale, old, old)
	mustContain(t, f.run(t, payloadOptions{}).Context, "root-local", "src-local")
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("sessions older than 30 days must be pruned")
	}
	// 壊れた記録は無かったものとして扱う。
	_ = os.WriteFile(path, []byte("{broken"), 0o600)
	mustContain(t, f.run(t, payloadOptions{}).Context, "root-local")
}

func TestContextLimit(t *testing.T) {
	f := newFixture(t)
	hooktest.WriteFile(t, filepath.Join(f.repo, "src", ruleName), strings.Repeat("x", maxContextBytes))
	injection := f.run(t, payloadOptions{})
	mustContain(t, injection.Context, "root-local")
	mustNotContain(t, injection.Context, "xxxx")
	mustContain(t, injection.SystemMessage, messages.Text(hooktest.Language, idContextLimit, map[string]any{"Limit": 32768, "Path": f.repo + "/src/AGENTS.local.md"}))
	// 未注入のルールは記録しないので、次の呼び出しでも警告する。
	mustContain(t, f.run(t, payloadOptions{}).SystemMessage, strings.Fields(messages.Text(hooktest.Language, idContextLimit, map[string]any{"Limit": 32768, "Path": ""}))[0])
}

func TestLockTimeoutFallsBack(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.cache, "hhx", Name)
	_ = os.MkdirAll(dir, 0o700)
	unlock, err := lockFile(filepath.Join(dir, ".lock"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	started := time.Now()
	injection := f.run(t, payloadOptions{})
	mustContain(t, injection.SystemMessage, warningKind(idStateClaim), "timed out")
	mustContain(t, injection.Context, "root-local")
	if elapsed := time.Since(started); elapsed < lockTimeout || elapsed > lockTimeout+5*time.Second {
		t.Fatalf("waited %s", elapsed)
	}
}

func TestArgvIsReadAsThePayload(t *testing.T) {
	f := newFixture(t)
	output := hooktest.Output(t, Definition(), "", hooktest.Options{Args: []string{string(mustJSON(f.payload(payloadOptions{})))}})
	mustContain(t, hooktest.ParseInjection(t, output).Context, "root-local")
}

func TestDisabledByConfig(t *testing.T) {
	f := newFixture(t)
	if output := hooktest.Output(t, Definition(), string(mustJSON(f.payload(payloadOptions{}))),
		hooktest.Options{Config: "hooks:\n  agents-local-context:\n    enabled: false\n"}); output != "" {
		t.Fatalf("output: %q", output)
	}
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

// --- 部品 ----------------------------------------------------------------------

func TestWarningText(t *testing.T) {
	if got := warningText(texts{hooktest.Language}, nil); got != "" {
		t.Errorf("no warnings: %q", got)
	}
	if got := warningText(texts{hooktest.Language}, []string{" a\n b ", "", "  ", "c", "d", "e", "f"}); got != warningPrefix+"a b; c; d; "+messages.Text(hooktest.Language, idMore, map[string]any{"Count": 2}) {
		t.Errorf("warningText=%q", got)
	}
}

func TestNormalizePath(t *testing.T) {
	f := newFixture(t)
	t.Setenv("HHX_TEST_DIR", f.nested)
	base := f.repo
	for _, testCase := range []struct {
		value    string
		explicit bool
		want     string
	}{
		{"src/service/target.py", false, f.target},
		{`"src/service/target.py"`, false, f.target},
		{"src/service/target.py:12", false, f.target},
		{"src/service/target.py:12:3", false, f.target},
		{"--file=src/service/target.py", false, f.target},
		{"-f=src", false, filepath.Join(f.repo, "src")},
		{"$HHX_TEST_DIR/target.py", false, f.target},
		{"${HHX_TEST_DIR}/x", false, filepath.Join(f.nested, "x")},
		{"src/*.go", false, filepath.Join(f.repo, "src")},
		{"*.go", false, f.repo},
		{"src//service/./target.py", false, f.target},
		{"src", false, filepath.Join(f.repo, "src")},
		{"./missing", false, filepath.Join(f.repo, "missing")},
		{"missing", true, filepath.Join(f.repo, "missing")},
		{"-flag", true, filepath.Join(f.repo, "-flag")},
		{"/abs/../x", false, "/x"},
		{"~/x", false, filepath.Join(f.tmp, "x")},
	} {
		got, ok := normalizePath(testCase.value, base, testCase.explicit)
		if !ok || got != testCase.want {
			t.Errorf("normalizePath(%q, %v)=%q %v, want %q", testCase.value, testCase.explicit, got, ok, testCase.want)
		}
	}
	for _, value := range []string{"", "  ", "-", "https://example.com/x", "-n", "missing", "'", "--"} {
		if got, ok := normalizePath(value, base, false); ok {
			t.Errorf("normalizePath(%q)=%q, want none", value, got)
		}
	}
}

func TestParentAndPartCount(t *testing.T) {
	for path, want := range map[string]string{"/a/b": "/a", "/a": "/", "/": "/", "//a": "//", "//": "//", "//a/b": "//a", "a": ".", "a/b": "a"} {
		if got := parent(path); got != want {
			t.Errorf("parent(%q)=%q, want %q", path, got, want)
		}
	}
	for path, want := range map[string]int{"/": 1, "/a": 2, "/a/b/AGENTS.local.md": 4} {
		if got := partCount(path); got != want {
			t.Errorf("partCount(%q)=%d, want %d", path, got, want)
		}
	}
}

func TestRulePathsForTarget(t *testing.T) {
	f := newFixture(t)
	roots := &gitRoots{cache: map[string]gitRootResult{}}
	paths, err := rulePathsForTarget(filepath.Join(f.nested, "missing", "deeper", "file.go"), roots)
	want := []string{filepath.Join(f.repo, ruleName), filepath.Join(f.repo, "src", ruleName), filepath.Join(f.nested, ruleName)}
	if err != nil || strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("paths=%q err=%v", paths, err)
	}
	if paths, _ := rulePathsForTarget(filepath.Join(f.tmp, "outside"), roots); len(paths) != 0 {
		t.Errorf("outside a repository: %q", paths)
	}
	if _, ok := roots.cache[f.nested]; !ok {
		t.Error("the git root must be memoized per directory")
	}
}

func TestCommandPathValues(t *testing.T) {
	values := commandPathValues("*** Add File: a.go\n--- b.go\n+++  c.go  \ncat 'd e' f")
	var got []string
	for _, value := range values {
		got = append(got, value.value)
	}
	if want := "a.go|b.go|c.go|***|Add|File:|a.go|---|b.go|+++|c.go|cat|d e|f"; strings.Join(got, "|") != want {
		t.Fatalf("values=%q", got)
	}
	if values := commandPathValues("echo 'unterminated"); len(values) != 0 {
		t.Fatalf("an unterminated quote must add no tokens: %v", values)
	}
}

// warningKind は警告の種類を見分ける部分（差し込みより前の文面）を、テストを流す言語でカタログから引く。
func warningKind(id string) string {
	entry := messages[id]
	text := entry.EN
	if hooktest.Language == i18n.Japanese {
		text = entry.JA
	}
	kind, _, _ := strings.Cut(text, "{{")
	return strings.TrimRight(kind, " :(")
}
