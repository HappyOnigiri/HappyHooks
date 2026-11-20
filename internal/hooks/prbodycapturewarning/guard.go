// Package prbodycapturewarning は、PR 本文と診断出力を混ぜて取り込む書き方を警告する。
package prbodycapturewarning

import (
	"bytes"
	"encoding/json"

	"github.com/HappyOnigiri/hhx/internal/hookrt"
)

// Name は hook の名前である。
const Name = "pr-body-capture-warning"

const (
	event        = "PreToolUse"
	contextLimit = 4096
	maxCodeBytes = 128 << 10
)

// Definition は pr-body-capture-warning の定義を返す。
func Definition() hookrt.Definition {
	return hookrt.Definition{
		Name: Name, DefaultEnabled: true,
		Registrations: []hookrt.Registration{
			{Agent: hookrt.Claude, Event: event, Matcher: "Bash"},
			{Agent: hookrt.Claude, Event: event, Matcher: "REPL"},
			{Agent: hookrt.Codex, Event: event, Matcher: "Bash", AdditionalContextLimit: contextLimit},
		},
		Gate: gate, Run: run,
	}
}

func gate(input []byte) bool {
	return bytes.Contains(input, []byte("gh")) && bytes.Contains(input, []byte("body")) &&
		(bytes.Contains(input, []byte(".text")) || bytes.Contains(input, []byte("&")) ||
			bytes.Contains(input, []byte(`\u0026`)) || bytes.Contains(input, []byte("2>")) ||
			bytes.Contains(input, []byte(`2\u003e`)))
}

func run(c *hookrt.Context) error {
	payload, err := c.Payload()
	if err != nil {
		return err
	}
	if payload.HookEventName != event {
		return nil
	}
	var input struct {
		Command string `json:"command"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(payload.ToolInput, &input); err != nil {
		return err
	}
	var id string
	switch payload.ToolName {
	case "Bash":
		if unsafeCapture(input.Command, false) {
			id = idMixedStreams
		}
	case "REPL":
		id = replWarning(input.Code)
	}
	if id != "" {
		c.AddContext(event, messages.T(c.Language(), id)+"\n"+messages.T(c.Language(), idAction))
	}
	return nil
}
