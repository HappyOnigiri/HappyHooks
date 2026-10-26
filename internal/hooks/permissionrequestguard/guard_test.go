package permissionrequestguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HappyOnigiri/hhx/internal/hookcache"
	"github.com/HappyOnigiri/hhx/internal/hooktest"
)

func TestMain(m *testing.M) {
	os.Exit(hooktest.Main(m))
}

// result は PermissionRequest の出力を読んだものである。無出力なら零値になる。
type result struct {
	Behavior string
	Message  string
}

func parse(t *testing.T, output string) result {
	t.Helper()
	if strings.TrimSpace(output) == "" {
		return result{}
	}
	var data struct {
		HookSpecificOutput struct {
			HookEventName string `json:"hookEventName"`
			Decision      struct {
				Behavior string `json:"behavior"`
				Message  string `json:"message"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&data); err != nil {
		t.Fatalf("output is not a PermissionRequest decision: %v: %q", err, output)
	}
	if data.HookSpecificOutput.HookEventName != "PermissionRequest" {
		t.Fatalf("hookEventName=%q", data.HookSpecificOutput.HookEventName)
	}
	return result{Behavior: data.HookSpecificOutput.Decision.Behavior, Message: data.HookSpecificOutput.Decision.Message}
}

// payload は Claude Code が Bash の PermissionRequest で渡す payload を作る（v2.1.310 で記録した形）。
func payload(command any, mode string, extra map[string]any) string {
	value := map[string]any{
		"session_id":             "test-session",
		"transcript_path":        "/dev/null",
		"cwd":                    "/Users/alice/work",
		"permission_mode":        mode,
		"hook_event_name":        "PermissionRequest",
		"tool_name":              "Bash",
		"tool_input":             map[string]any{"command": command, "description": "d"},
		"permission_suggestions": []any{},
	}
	for key, item := range extra {
		value[key] = item
	}
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func invoke(t *testing.T, raw string, options hooktest.Options) result {
	t.Helper()
	return parse(t, hooktest.Output(t, Definition(), raw, options))
}

// isolateCache は記録の置き場をテストごとの一時ディレクトリにし、記録のパスを返す。
func isolateCache(t *testing.T) string {
	t.Helper()
	cache := hooktest.TempDir(t)
	t.Setenv("XDG_CACHE_HOME", cache)
	return filepath.Join(cache, "hhx", Name, logName)
}

func readLog(t *testing.T, path string) []entry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entries []entry
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var value entry
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		entries = append(entries, value)
	}
	return entries
}

func TestDeniesEveryBashRequest(t *testing.T) {
	isolateCache(t)
	why := map[string]string{
		"bypassPermissions": messages.T(hooktest.Language, idWhyBypass),
		"default":           messages.T(hooktest.Language, idWhyApproval),
		"acceptEdits":       messages.T(hooktest.Language, idWhyApproval),
		"plan":              messages.T(hooktest.Language, idWhyApproval),
		"":                  messages.T(hooktest.Language, idWhyApproval),
	}
	// コマンドの中身は問わない（rm を含まない形・普通のコマンド・空文字列も止める）。
	for _, command := range []string{"rmdir ~", `echo ${(f)"$(ls)"}`, "touch x", "", "rm -rf a\n"} {
		for mode, want := range why {
			got := invoke(t, payload(command, mode, nil), hooktest.Options{})
			if got.Behavior != "deny" {
				t.Errorf("command %q mode %q: behavior=%q", command, mode, got.Behavior)
				continue
			}
			if !strings.Contains(got.Message, want) || !strings.Contains(got.Message, messages.T(hooktest.Language, idHow)) {
				t.Errorf("command %q mode %q: message %q", command, mode, got.Message)
			}
		}
	}
}

func TestRecordsDeniedCommands(t *testing.T) {
	path := isolateCache(t)
	now = func() time.Time { return time.Date(2026, 10, 27, 1, 2, 3, 0, time.FixedZone("JST", 9*3600)) }
	t.Cleanup(func() { now = time.Now })
	invoke(t, payload("cd junk && rm -rf .", "bypassPermissions", nil), hooktest.Options{})
	invoke(t, payload("echo '<a&b>'", "default", map[string]any{
		"agent_id": "a1", "agent_type": "general-purpose",
		"permission_suggestions": []any{map[string]any{"type": "setMode", "mode": "acceptEdits", "destination": "session"}},
	}), hooktest.Options{})
	entries := readLog(t, path)
	if len(entries) != 2 {
		t.Fatalf("entries=%v", entries)
	}
	first := entries[0]
	if first.Time != "2026-10-26T16:02:03Z" || first.Command != "cd junk && rm -rf ." || first.CWD != "/Users/alice/work" ||
		first.PermissionMode != "bypassPermissions" || first.SessionID != "test-session" || first.HHXVersion == "" ||
		first.AgentID != "" || string(first.Suggestions) != "[]" {
		t.Errorf("first=%+v", first)
	}
	if second := entries[1]; second.AgentID != "a1" || second.AgentType != "general-purpose" || second.Command != "echo '<a&b>'" ||
		string(second.Suggestions) != `[{"destination":"session","mode":"acceptEdits","type":"setMode"}]` {
		t.Errorf("second=%+v", second)
	}
	// 調べる人が読めるよう、`<>&` はエスケープしない。
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), `"echo '<a&b>'"`) {
		t.Errorf("log=%q", data)
	}
	dir, _ := os.Stat(filepath.Dir(path))
	file, _ := os.Stat(path)
	if dir.Mode().Perm() != 0o700 || file.Mode().Perm() != 0o600 {
		t.Errorf("modes: dir=%v file=%v", dir.Mode(), file.Mode())
	}
}

// payload に permission_suggestions が無ければ、記録にも書かない（null を書かない）。
func TestOmitsMissingSuggestions(t *testing.T) {
	path := isolateCache(t)
	invoke(t, `{"tool_name":"Bash","permission_mode":"default","tool_input":{"command":"touch x"}}`, hooktest.Options{})
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "permission_suggestions") {
		t.Fatalf("log=%q err=%v", data, err)
	}
}

func TestRotatesALargeLog(t *testing.T) {
	path := isolateCache(t)
	if err := hookcache.WriteFile(filepath.Dir(path), logName, []byte(strings.Repeat("x", maxLogSize+1))); err != nil {
		t.Fatal(err)
	}
	invoke(t, payload("touch x", "default", nil), hooktest.Options{})
	if entries := readLog(t, path); len(entries) != 1 || entries[0].Command != "touch x" {
		t.Errorf("entries=%v", entries)
	}
	if info, err := os.Stat(path + ".1"); err != nil || info.Size() != maxLogSize+1 {
		t.Errorf("rotated: %v %v", info, err)
	}
}

// 記録できなくても deny は出す。
func TestDeniesEvenIfTheLogFails(t *testing.T) {
	blocker := filepath.Join(hooktest.TempDir(t), "file")
	hooktest.WriteFile(t, blocker, "")
	t.Setenv("XDG_CACHE_HOME", blocker)
	if got := invoke(t, payload("touch x", "default", nil), hooktest.Options{}); got.Behavior != "deny" {
		t.Fatalf("got %+v", got)
	}
}

func TestPassesInputsItCannotRead(t *testing.T) {
	path := isolateCache(t)
	for _, raw := range []string{
		"", "null", "[]", "not json", `{"tool_name":"Bash","tool_input":"x"}`,
		payload(nil, "default", nil), payload(42, "default", nil),
		payload("touch x", "default", map[string]any{"tool_name": "Write"}),
	} {
		if got := invoke(t, raw, hooktest.Options{}); got != (result{}) {
			t.Errorf("input %q: got %+v", raw, got)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("nothing must be recorded: %v", err)
	}
}

func TestDisabledByConfig(t *testing.T) {
	path := isolateCache(t)
	got := invoke(t, payload("touch x", "default", nil), hooktest.Options{Config: "hooks:\n  " + Name + ":\n    enabled: false\n"})
	if got != (result{}) {
		t.Fatalf("got %+v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("nothing must be recorded: %v", err)
	}
}

// デバッグ経路は理由文だけを出し、記録しない。
func TestArgvShowsTheReasonWithoutRecording(t *testing.T) {
	path := isolateCache(t)
	got := invoke(t, "", hooktest.Options{Args: []string{"rm -rf ~"}})
	if got.Behavior != "deny" || !strings.Contains(got.Message, messages.T(hooktest.Language, idWhyApproval)) {
		t.Fatalf("got %+v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("nothing must be recorded: %v", err)
	}
}
