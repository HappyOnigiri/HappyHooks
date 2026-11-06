// Package blockingcommandguard は、字面から分かる入力待ち・対話操作・継続実行を事前に拒否する。
package blockingcommandguard

import (
	"encoding/json"
	"strings"

	"github.com/HappyOnigiri/hhx/internal/hookrt"
	"github.com/HappyOnigiri/hhx/internal/pycompat"
)

// Name は hook の名前である。
const Name = "blocking-command-guard"

// Definition は blocking-command-guard の定義を返す。
func Definition() hookrt.Definition {
	return hookrt.Definition{
		Name: Name, DefaultEnabled: true,
		Registrations: []hookrt.Registration{
			{Agent: hookrt.Claude, Event: "PreToolUse", Matcher: "Bash"},
			{Agent: hookrt.Codex, Event: "PreToolUse", Matcher: "Bash"},
		},
		Gate: gate, Run: run,
	}
}

// gateKeywords は解析対象の語。設定の読み込みや構文解析より前に絞る。
var gateKeywords = []string{
	"cat", "tee", "sort", "wc", "head", "tail", "grep", "sed", "awk", "read", "python", "node", "ruby", "perl",
	"bash", "zsh", "git", "vi", "nano", "emacs", "watch", "top", "fzf", "yes",
}

func gate(input []byte) bool {
	raw := string(input)
	for _, word := range gateKeywords {
		if strings.Contains(raw, word) {
			return true
		}
	}
	// sh だけは左境界を見る。単なる部分一致だと全 payload の tool_name: Bash に当たる。
	for offset := 0; offset < len(raw); {
		index := strings.Index(raw[offset:], "sh")
		if index < 0 {
			break
		}
		index += offset
		if index == 0 || !identifierByte(raw[index-1]) {
			return true
		}
		offset = index + 2
	}
	return false
}

func identifierByte(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
		(char >= '0' && char <= '9') || char == '_'
}

func run(c *hookrt.Context) error {
	command := string(c.Input)
	if !c.FromArgs {
		var payload struct {
			ToolInput struct {
				Command         string `json:"command"`
				RunInBackground bool   `json:"run_in_background"`
			} `json:"tool_input"`
		}
		if err := json.Unmarshal(c.Input, &payload); err != nil {
			return err
		}
		if payload.ToolInput.RunInBackground {
			return nil
		}
		command = payload.ToolInput.Command
	}
	if label := classify(command); label != "" {
		c.Deny(messages.Text(c.Language(), idReason, map[string]any{
			"Label": label, "Command": pycompat.QuoteJSON(command),
		}))
	}
	return nil
}
