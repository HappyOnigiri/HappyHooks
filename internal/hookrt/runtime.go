package hookrt

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/HappyOnigiri/hhx/internal/config"
	"github.com/HappyOnigiri/hhx/internal/i18n"
)

// maxInputSize は stdin から読む payload の上限である。超えた分は読まず、切り詰めた入力は hook に渡さない。
const maxInputSize = 16 << 20

// Invocation は `hhx hook <name>` の 1 回分の入力である。
type Invocation struct {
	// Args は <name> より後ろの引数である。1 つ目があれば stdin の代わりに入力として使う（デバッグ用の経路）。
	Args   []string
	Stdin  io.Reader
	Stdout io.Writer
	// LoadConfig は設定を読む。エラーは既定値へ退避させるので、hook は止まらない。
	LoadConfig func() (*config.Config, error)
}

// Run は definition を実行する。definition が nil（未知の hook 名）なら何もしない。
// どの経路でも、呼び出し側は終了コード 0 で終わる。出力は本体が正常に戻ったときだけ書き出す。
func Run(definition *Definition, invocation Invocation) {
	defer func() {
		// panic は fail-open で握りつぶす。途中まで組み立てた出力は書かない。
		_ = recover()
	}()
	if definition == nil || definition.Run == nil {
		return
	}
	input, fromArgs, ok := readInput(invocation)
	if !ok {
		return
	}
	gated := definition.Gate != nil && (!fromArgs || !definition.GateStdinOnly)
	if gated && !definition.Gate(input) {
		return
	}
	cfg := &config.Config{}
	if invocation.LoadConfig != nil {
		if loaded, err := invocation.LoadConfig(); err == nil && loaded != nil {
			cfg = loaded
		}
	}
	if !cfg.Enabled(definition.Name, definition.DefaultEnabled) {
		return
	}
	context := &Context{Name: definition.Name, Input: input, FromArgs: fromArgs, config: cfg}
	if err := definition.Run(context); err != nil {
		return
	}
	if context.output != nil && invocation.Stdout != nil {
		_, _ = invocation.Stdout.Write(context.output)
	}
}

func readInput(invocation Invocation) (input []byte, fromArgs, ok bool) {
	if len(invocation.Args) > 0 {
		return []byte(invocation.Args[0]), true, true
	}
	if invocation.Stdin == nil {
		return nil, false, true
	}
	data, err := io.ReadAll(io.LimitReader(invocation.Stdin, maxInputSize+1))
	if err != nil || len(data) > maxInputSize {
		return nil, false, false
	}
	return data, false, true
}

// Context は hook の本体に渡す入力と、出力の組み立て先である。
type Context struct {
	Name string
	// Input は stdin の payload、またはデバッグ経路で渡された引数そのものである。
	Input []byte
	// FromArgs は Input が引数から来たかを示す。引数の意味（payload かコマンド文字列か）は hook ごとに決める。
	FromArgs bool
	config   *config.Config
	output   []byte
}

// Payload は agent が stdin に渡す JSON のうち、複数の hook が使う項目である。
// イベント固有の項目は ToolInput を hook 側で読むか、Context.Input を直接読む。
type Payload struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	CWD            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	Prompt         string          `json:"prompt"`
	Source         string          `json:"source"`
}

// Payload は Input を JSON として読む。
func (c *Context) Payload() (Payload, error) {
	var payload Payload
	err := json.Unmarshal(c.Input, &payload)
	return payload, err
}

// Language は文面に使う表示言語を返す。設定を読むのは一次ゲートより後なので、ゲートで抜ける呼び出しは費用を払わない。
func (c *Context) Language() i18n.Language {
	return c.config.DisplayLanguage()
}

// Settings は設定ファイルにあるこの hook の mapping を value へ読み込む。設定が無ければ value を変えない。
func (c *Context) Settings(value any) error {
	return c.config.Decode(c.Name, value)
}

// Deny は PreToolUse の拒否を出力する。通すときは何も出力しない（allow は返さない）。
// `ask` は Codex が解釈しないため用意しない。
func (c *Context) Deny(reason string) {
	c.setJSON(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       "deny",
		"permissionDecisionReason": reason,
	}})
}

// DenyPermission は PermissionRequest の拒否を出力する。message は理由文として Claude に渡る。
// interrupt は付けない（付けると Claude の作業ごと止まる）。PermissionRequest は Claude Code にしか無い。
func (c *Context) DenyPermission(message string) {
	c.setJSON(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PermissionRequest",
		"decision": map[string]any{
			"behavior": "deny",
			"message":  message,
		},
	}})
}

// AddContext は event の additionalContext としてコンテキストへ注入する文字列を出力する。
func (c *Context) AddContext(event, text string) {
	c.setJSON(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":     event,
		"additionalContext": text,
	}})
}

// Notify は systemMessage（利用者への警告）と、event の additionalContext を出力する。空の項目は書かない。
// 両方が空なら何も出力しない。判断のフィールド（deny など）は持たない。
func (c *Context) Notify(event, text, systemMessage string) {
	type specific struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	}
	var output struct {
		SystemMessage      string    `json:"systemMessage,omitempty"`
		HookSpecificOutput *specific `json:"hookSpecificOutput,omitempty"`
	}
	output.SystemMessage = systemMessage
	if text != "" {
		output.HookSpecificOutput = &specific{HookEventName: event, AdditionalContext: text}
	}
	if systemMessage == "" && text == "" {
		c.output = nil
		return
	}
	c.setJSON(output)
}

// Print は平文を出力する。UserPromptSubmit では stdout の平文がそのままコンテキストに入る。
func (c *Context) Print(text string) {
	c.output = []byte(text)
}

// setJSON は出力を value の JSON で置き換える。1 回の実行で出力は 1 つだけなので、後の呼び出しが勝つ。
func (c *Context) setJSON(value any) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		panic(err)
	}
	c.output = out.Bytes()
}
