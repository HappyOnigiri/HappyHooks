package registry

import (
	"regexp"
	"testing"

	"github.com/HappyOnigiri/hhx/internal/hookrt"
)

var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// 各 CLI で登録を確認済みのイベント名。綴りを誤ったイベントは CLI に黙って無視されるため、ここにあるものだけを許す。
// 別のイベントへ登録する hook を足すときは、その CLI が受け付けることを確かめてから追加する。
var knownEvents = map[hookrt.Agent]map[string]bool{
	hookrt.Claude: {"PermissionRequest": true, "PreToolUse": true, "PostToolUse": true, "UserPromptSubmit": true, "SessionStart": true, "SessionEnd": true},
	hookrt.Codex:  {"PreToolUse": true, "PostToolUse": true, "UserPromptSubmit": true, "SessionStart": true, "SessionEnd": true, "SubagentStart": true},
}

// TestDefinitionsAreWellFormed は登録表の不変条件を検査する。hook を足すたびにこのテストが守る。
func TestDefinitionsAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, definition := range All() {
		if !namePattern.MatchString(definition.Name) {
			t.Errorf("hook name %q must be lowercase words joined by hyphens", definition.Name)
		}
		if seen[definition.Name] {
			t.Errorf("hook name %q is registered twice", definition.Name)
		}
		seen[definition.Name] = true
		if definition.Run == nil {
			t.Errorf("hook %q has no Run", definition.Name)
		}
		if len(definition.Registrations) == 0 {
			t.Errorf("hook %q is not registered to any agent", definition.Name)
		}
		pairs := map[hookrt.Registration]bool{}
		for _, registration := range definition.Registrations {
			if !knownEvents[registration.Agent][registration.Event] {
				t.Errorf("hook %q: %s does not support event %q", definition.Name, registration.Agent, registration.Event)
			}
			if registration.Timeout < 0 || registration.AdditionalContextLimit < 0 {
				t.Errorf("hook %q: negative timeout or additionalContextLimit", definition.Name)
			}
			if registration.Agent == hookrt.Claude && registration.AdditionalContextLimit != 0 {
				t.Errorf("hook %q: additionalContextLimit is a Codex-only field", definition.Name)
			}
			key := hookrt.Registration{Agent: registration.Agent, Event: registration.Event, Matcher: registration.Matcher}
			if pairs[key] {
				t.Errorf("hook %q is registered twice to %s %s %q", definition.Name, registration.Agent, registration.Event, registration.Matcher)
			}
			pairs[key] = true
		}
	}
}

func TestLookupReturnsNilForUnknownName(t *testing.T) {
	if Lookup("no-such-hook") != nil || Known("no-such-hook") {
		t.Fatal("unknown hook name must not resolve")
	}
}
