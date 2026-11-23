// Package prbodyroundtrip は送信したファイルと GitHub 上の PR 本文の差分を知らせる。
package prbodyroundtrip

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HappyOnigiri/hhx/internal/hookexec"
	"github.com/HappyOnigiri/hhx/internal/hookrt"
	"github.com/HappyOnigiri/hhx/internal/toolresponse"
)

// Name は hook の名前である。
const Name = "pr-body-roundtrip"

const (
	event        = "PostToolUse"
	maxBodyBytes = 1 << 20
)

var fetchTimeout = 4 * time.Second

// Definition は pr-body-roundtrip の定義を返す。
func Definition() hookrt.Definition {
	return hookrt.Definition{
		Name: Name, DefaultEnabled: true,
		Registrations: []hookrt.Registration{
			{Agent: hookrt.Claude, Event: event, Matcher: "Bash", Timeout: 10},
		},
		Gate: gate, Run: run,
	}
}

func gate(input []byte) bool {
	return bytes.Contains(input, []byte("gh")) && bytes.Contains(input, []byte("pr")) &&
		(bytes.Contains(input, []byte("--body-file")) || bytes.Contains(input, []byte("-F")))
}

type payload struct {
	Event     string `json:"hook_event_name"`
	Tool      string `json:"tool_name"`
	Dir       string `json:"cwd"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
	Response any `json:"tool_response"`
}

func run(c *hookrt.Context) error {
	var p payload
	decoder := json.NewDecoder(bytes.NewReader(c.Input))
	decoder.UseNumber()
	if err := decoder.Decode(&p); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF || p.Event != event || p.Tool != "Bash" ||
		!toolresponse.Succeeded(p.Response) {
		return nil
	}
	req, ok := parseCommand(p.ToolInput.Command, p.Dir)
	if !ok {
		return nil
	}
	stdout, ok := responseOutput(p.Response)
	if !ok || req.checkStatus && !strings.HasSuffix("\n"+strings.TrimSpace(stdout), "\nrc=0") {
		return nil
	}
	if req.create {
		req.selector = createdPR(stdout)
		req.repo = ""
		if req.selector == "" {
			return nil
		}
	}
	sent, ok := readBody(req)
	if !ok {
		return nil
	}
	current, ok := currentBody(req)
	if !ok {
		return nil
	}
	sent, current = normalizeBody(sent), normalizeBody(current)
	if sent == current {
		return nil
	}
	diff, shortened := diffPreview(current, sent)
	if shortened {
		diff += messages.T(c.Language(), idShortened)
	}
	c.AddContext(event, messages.Text(c.Language(), idMismatch, map[string]any{"Diff": diff}))
	return nil
}

func responseOutput(response any) (string, bool) {
	switch response := response.(type) {
	case string:
		return response, true
	case map[string]any:
		for _, key := range []string{"stdout", "output", "aggregated_output"} {
			if value, ok := response[key].(string); ok {
				return value, true
			}
		}
		return "", true
	default:
		return "", false
	}
}

// createdPR は stdout の PR URL が一意に決まるときに限り、その URL を返す。
func createdPR(stdout string) string {
	var selector string
	for line := range strings.SplitSeq(stdout, "\n") {
		line = strings.TrimSpace(line)
		u, err := url.Parse(line)
		if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Hostname() == "" || u.User != nil {
			continue
		}
		parts := strings.Split(u.Path, "/")
		if len(parts) != 5 || parts[1] == "" || parts[2] == "" || parts[3] != "pull" {
			continue
		}
		if number, err := strconv.ParseUint(parts[4], 10, 64); err != nil || number == 0 {
			continue
		}
		if selector != "" && selector != line {
			return ""
		}
		selector = line
	}
	return selector
}

func readBody(req request) (string, bool) {
	path := req.bodyFile
	if !filepath.IsAbs(path) {
		path = req.dir + string(filepath.Separator) + path
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
	body, err := io.ReadAll(io.LimitReader(file, maxBodyBytes+1))
	return string(body), err == nil && len(body) <= maxBodyBytes && utf8.Valid(body)
}

func currentBody(req request) (string, bool) {
	args := []string{"pr", "view"}
	var env []string
	if req.selector != "" {
		args = append(args, req.selector)
		if req.repo != "" {
			args = append(args, "--repo", req.repo)
		}
	} else if req.repo != "" {
		// gh pr view は --repo を指定すると PR の引数を要求する。
		env = []string{"GH_REPO=" + req.repo}
	}
	args = append(args, "--json", "body", "--jq", ".body")
	body, ok := hookexec.OutputWithEnv(req.dir, fetchTimeout, env, "gh", args...)
	return body, ok && len(body) <= maxBodyBytes && utf8.ValidString(body)
}

func normalizeBody(body string) string {
	return strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
}
