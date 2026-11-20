// Package prbodyeditguard は、PR 本文の先頭・末尾に混ざった診断出力を送信前に拒否する。
package prbodyeditguard

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HappyOnigiri/hhx/internal/hookexec"
	"github.com/HappyOnigiri/hhx/internal/hookrt"
)

// Name は hook の名前である。
const Name = "pr-body-edit-guard"

const (
	event        = "PreToolUse"
	ackVariable  = "HHX_PR_BODY_ACK"
	maxBodyBytes = 1 << 20
	fetchTimeout = 4 * time.Second
)

// Definition は pr-body-edit-guard の定義を返す。
func Definition() hookrt.Definition {
	return hookrt.Definition{
		Name: Name, DefaultEnabled: true,
		Registrations: []hookrt.Registration{
			{Agent: hookrt.Claude, Event: event, Matcher: "Bash", Timeout: 10},
			{Agent: hookrt.Codex, Event: event, Matcher: "Bash", Timeout: 10},
		},
		Gate: gate, Run: run,
	}
}

func gate(input []byte) bool {
	return bytes.Contains(input, []byte("gh")) && bytes.Contains(input, []byte("edit")) &&
		(bytes.Contains(input, []byte("body")) || bytes.Contains(input, []byte("-F")) || bytes.Contains(input, []byte("-b")))
}

func run(c *hookrt.Context) error {
	payload, err := c.Payload()
	if err != nil {
		return err
	}
	if payload.HookEventName != event || payload.ToolName != "Bash" {
		return nil
	}
	var input struct {
		Command string `json:"command"`
		Workdir string `json:"workdir"`
	}
	if err := json.Unmarshal(payload.ToolInput, &input); err != nil {
		return err
	}
	dir := payload.CWD
	if input.Workdir != "" {
		dir = input.Workdir
	}
	edit, ok := parseEdit(input.Command, dir)
	if !ok {
		return nil
	}
	proposed, ok := edit.body()
	if !ok {
		return nil
	}
	// ACK は gh の起動前に完全一致で確認する。セッションやフックプロセスの環境からは読まない。
	if edit.ack != "" && edit.ack == bodyDigest(proposed) {
		return nil
	}
	// 診断出力の候補がない本文では、比較のために GitHub を読まない。
	lines := splitLines(proposed)
	excluded := excludedLines(lines)
	if firstFinding(lines, excluded, 0, len(lines)) == nil {
		return nil
	}
	current, ok := currentBody(edit)
	if !ok {
		return nil
	}
	findings := edgeFindings(current, lines, excluded)
	if len(findings) > 0 {
		c.Deny(reason(c.Language(), findings, bodyDigest(proposed)))
	}
	return nil
}

func bodyDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func (e editRequest) body() (string, bool) {
	if e.bodyFile == "" {
		return e.bodyText, len(e.bodyText) <= maxBodyBytes && utf8.ValidString(e.bodyText)
	}
	path := e.bodyFile
	if !filepath.IsAbs(path) {
		path = e.dir + string(filepath.Separator) + path
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBodyBytes {
		return "", false
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxBodyBytes+1))
	if err != nil || len(data) > maxBodyBytes || !utf8.Valid(data) {
		return "", false
	}
	return string(data), true
}

func currentBody(edit editRequest) (string, bool) {
	args := []string{"pr", "view"}
	var env []string
	if edit.selector != "" {
		// 先頭が - のセレクタは parseEdit が受け付けない。
		args = append(args, edit.selector)
		if edit.repo != "" {
			args = append(args, "--repo", edit.repo)
		}
	} else if edit.repo != "" {
		// view は --repo とセレクタ省略を併用できない。GH_REPO なら現在のブランチから PR を解決する。
		env = []string{"GH_REPO=" + edit.repo}
	}
	args = append(args, "--json", "body")
	output, ok := hookexec.OutputWithEnv(edit.dir, fetchTimeout, env, "gh", args...)
	if !ok {
		return "", false
	}
	var result struct {
		Body *string `json:"body"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil || result.Body == nil {
		return "", false
	}
	return *result.Body, len(*result.Body) <= maxBodyBytes
}

func splitLines(body string) []string {
	return strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
}
