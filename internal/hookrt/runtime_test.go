package hookrt

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HappyOnigiri/hhx/internal/config"
)

func invoke(definition *Definition, stdin string, args ...string) string {
	var out bytes.Buffer
	Run(definition, Invocation{Args: args, Stdin: strings.NewReader(stdin), Stdout: &out})
	return out.String()
}

func TestUnknownHookProducesNothing(t *testing.T) {
	if got := invoke(nil, `{"tool_name":"Bash"}`); got != "" {
		t.Fatalf("unknown hook wrote %q", got)
	}
}

func TestDenyOutputSchema(t *testing.T) {
	definition := &Definition{Name: "d", DefaultEnabled: true, Run: func(c *Context) error {
		payload, err := c.Payload()
		if err != nil {
			return err
		}
		c.Deny("blocked " + payload.ToolName + " <&>")
		return nil
	}}
	var got struct {
		HookSpecificOutput map[string]string `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(invoke(definition, `{"tool_name":"Bash"}`)), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": "blocked Bash <&>"}
	for key, value := range want {
		if got.HookSpecificOutput[key] != value {
			t.Fatalf("%s=%q, want %q", key, got.HookSpecificOutput[key], value)
		}
	}
}

// PermissionRequest の拒否は decision.behavior で返し、interrupt は付けない。
func TestDenyPermissionOutputSchema(t *testing.T) {
	definition := &Definition{Name: "d", DefaultEnabled: true, Run: func(c *Context) error {
		c.DenyPermission("blocked <&>")
		return nil
	}}
	output := invoke(definition, `{"tool_name":"Bash"}`)
	want := `{"hookSpecificOutput":{"decision":{"behavior":"deny","message":"blocked <&>"},"hookEventName":"PermissionRequest"}}` + "\n"
	if output != want {
		t.Fatalf("output=%q, want %q", output, want)
	}
}

func TestAddContextAndPrint(t *testing.T) {
	add := &Definition{Name: "a", DefaultEnabled: true, Run: func(c *Context) error {
		c.AddContext("PostToolUse", "note")
		return nil
	}}
	if got := invoke(add, "{}"); got != `{"hookSpecificOutput":{"additionalContext":"note","hookEventName":"PostToolUse"}}`+"\n" {
		t.Fatalf("AddContext wrote %q", got)
	}
	printer := &Definition{Name: "p", DefaultEnabled: true, Run: func(c *Context) error {
		c.Print("plain text")
		return nil
	}}
	if got := invoke(printer, "{}"); got != "plain text" {
		t.Fatalf("Print wrote %q", got)
	}
}

func TestNotifyWritesOnlyTheGivenParts(t *testing.T) {
	for _, testCase := range []struct {
		text, message, want string
	}{
		{"ctx", "", `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"ctx <&>"}}`},
		{"", "warn", `{"systemMessage":"warn <&>"}`},
		{"ctx", "warn", `{"systemMessage":"warn <&>","hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"ctx <&>"}}`},
		{"", "", ""},
	} {
		definition := &Definition{Name: "n", DefaultEnabled: true, Run: func(c *Context) error {
			c.Deny("replaced")
			text, message := testCase.text, testCase.message
			if text != "" {
				text += " <&>"
			}
			if message != "" {
				message += " <&>"
			}
			c.Notify("SessionStart", text, message)
			return nil
		}}
		want := testCase.want
		if want != "" {
			want += "\n"
		}
		if got := invoke(definition, "{}"); got != want {
			t.Errorf("Notify(%q, %q) wrote %q, want %q", testCase.text, testCase.message, got, want)
		}
	}
}

func TestFailuresAreSilent(t *testing.T) {
	for name, run := range map[string]func(*Context) error{
		"error": func(c *Context) error {
			c.Deny("partial")
			return errors.New("boom")
		},
		"panic": func(c *Context) error {
			c.Deny("partial")
			panic("boom")
		},
	} {
		definition := &Definition{Name: name, DefaultEnabled: true, Run: run}
		if got := invoke(definition, "{}"); got != "" {
			t.Errorf("%s: a failing hook must write nothing, wrote %q", name, got)
		}
	}
}

func TestGateRunsBeforeConfig(t *testing.T) {
	loaded := false
	ran := false
	definition := &Definition{
		Name: "g", DefaultEnabled: true,
		Gate: func(input []byte) bool { return bytes.Contains(input, []byte("gh ")) },
		Run: func(*Context) error {
			ran = true
			return nil
		},
	}
	Run(definition, Invocation{Stdin: strings.NewReader(`{"command":"ls"}`), LoadConfig: func() (*config.Config, error) {
		loaded = true
		return &config.Config{}, nil
	}})
	if loaded || ran {
		t.Fatal("a closed gate must skip both config loading and the hook body")
	}
}

func TestGateStdinOnlySkipsTheGateForArgs(t *testing.T) {
	for _, stdinOnly := range []bool{false, true} {
		ran := false
		definition := &Definition{
			Name: "g", DefaultEnabled: true, GateStdinOnly: stdinOnly,
			Gate: func(input []byte) bool { return bytes.Contains(input, []byte("ExitPlanMode")) },
			Run: func(*Context) error {
				ran = true
				return nil
			},
		}
		invoke(definition, "", "/tmp/transcript.jsonl")
		if ran != stdinOnly {
			t.Errorf("GateStdinOnly=%v: ran=%v with args", stdinOnly, ran)
		}
		// stdin の入力には、どちらでもゲートを当てる。
		ran = false
		invoke(definition, `{"tool_name":"Bash"}`)
		if ran {
			t.Errorf("GateStdinOnly=%v: the gate must close for stdin", stdinOnly)
		}
	}
}

func TestConfigDecidesEnabled(t *testing.T) {
	disabled, enabled := false, true
	cfg := &config.Config{Hooks: map[string]config.Hook{
		"off": {Enabled: &disabled},
		"on":  {Enabled: &enabled},
	}}
	for _, test := range []struct {
		name      string
		byDefault bool
		want      bool
	}{
		{name: "off", byDefault: true, want: false},
		{name: "on", byDefault: false, want: true},
		{name: "unset", byDefault: false, want: false},
	} {
		ran := false
		definition := &Definition{Name: test.name, DefaultEnabled: test.byDefault, Run: func(*Context) error {
			ran = true
			return nil
		}}
		Run(definition, Invocation{Stdin: strings.NewReader("{}"), LoadConfig: func() (*config.Config, error) { return cfg, nil }})
		if ran != test.want {
			t.Errorf("%s: ran=%v, want %v", test.name, ran, test.want)
		}
	}
}

func TestBrokenConfigFallsBackToDefaults(t *testing.T) {
	ran := false
	definition := &Definition{Name: "x", DefaultEnabled: true, Run: func(*Context) error {
		ran = true
		return nil
	}}
	Run(definition, Invocation{Stdin: strings.NewReader("{}"), LoadConfig: func() (*config.Config, error) {
		return nil, errors.New("broken")
	}})
	if !ran {
		t.Fatal("a broken config must not stop a hook that is enabled by default")
	}
}

func TestArgumentReplacesStdin(t *testing.T) {
	var input string
	var fromArgs bool
	definition := &Definition{Name: "a", DefaultEnabled: true, Run: func(c *Context) error {
		input, fromArgs = string(c.Input), c.FromArgs
		return nil
	}}
	invoke(definition, "ignored", "gh pr merge 1")
	if input != "gh pr merge 1" || !fromArgs {
		t.Fatalf("input=%q fromArgs=%v", input, fromArgs)
	}
	invoke(definition, "from stdin")
	if input != "from stdin" || fromArgs {
		t.Fatalf("input=%q fromArgs=%v", input, fromArgs)
	}
}

func TestSettingsReadsTheHookMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("hooks:\n  s:\n    message: hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Message string `yaml:"message"`
	}
	definition := &Definition{Name: "s", DefaultEnabled: true, Run: func(c *Context) error {
		return c.Settings(&settings)
	}}
	Run(definition, Invocation{Stdin: strings.NewReader("{}"), LoadConfig: func() (*config.Config, error) {
		return config.Load(path)
	}})
	if settings.Message != "hello" {
		t.Fatalf("settings=%+v", settings)
	}
}
