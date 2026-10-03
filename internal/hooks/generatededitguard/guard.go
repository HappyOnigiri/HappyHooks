// Package generatededitguard は、自動生成ファイルの手編集を PreToolUse で拒否する hook（generated-edit-guard）である。
//
// 方針:
//   - 生成ファイルはパスの glob ではなく、ファイルの先頭のマーカーで判定する（Go の標準のマーカーと、設定の markers）。
//     生成物の置き場が変わっても追従でき、リポジトリごとの一覧も要らない。存在しないパス（新規作成）は通す。
//   - 編集ツール（Edit・Write・MultiEdit・NotebookEdit・apply_patch）の対象と、Bash のコマンドの書き込み先（sed -i、tee、cp、
//     patch、インタプリタにその場で渡したコード、手で文面を作るコマンドの出力のリダイレクトなど）を判定に掛ける。
//     外部のスクリプトファイルの中の書き込みと、生成器やスクリプトの出力のリダイレクトは、作り直しの正規の形なので見ない。
//   - 判定は deny だけで、ask は返さない（自律開発が主な用途で、Codex は ask を解釈しない）。理由文は生成コマンドでの作り直しへ誘導し、
//     ユーザーへの確認は促さない。
//   - ユーザーが会話の中で明示的に指示したときだけ、エージェントが `hhx allow-generated-edit` で宣言し、hook がそれを payload の
//     session_id ごとに記録して、同じセッションの宣言したパスへの編集を通す。記録は PostToolUse で、実行に成功した宣言だけを扱う
//     （利用者が実行を拒んだ宣言や、実行されなかった分岐の宣言を残さない）。宣言を受け付けたときと、宣言で編集を通すたびに、
//     最終報告に載せる内容を判断のフィールドを持たない additionalContext で注入する。
//   - cp・mv・install の送り元がすべて生成ファイルか、同じコマンドで生成器が出力したファイルなら、作り直した結果を置く形として通す。
//
// Codex の PreToolUse には exec_command の workdir が渡らず、payload の cwd はセッション開始時のディレクトリのままである。
// Bash の相対パスは、コマンドの中の cd を辿って解決する。デバッグ経路では、引数が JSON の object なら payload として、
// そうでなければ Bash のコマンド文字列として読み、プロセスの作業ディレクトリを基準にする（session_id が無いので宣言は記録しない）。
package generatededitguard

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/HappyOnigiri/hhx/internal/hookrt"
	py "github.com/HappyOnigiri/hhx/internal/pycompat"
	"github.com/HappyOnigiri/hhx/internal/toolresponse"
)

// Name は hook の名前である。
const Name = "generated-edit-guard"

// contextLimit は Codex の additionalContextLimit である。注入の文面は件数と長さを切り詰めて、この中に収める。
const contextLimit = 4096

// Definition は generated-edit-guard の定義を返す。登録先は irreversible-guard と同じ matcher にする。
func Definition() hookrt.Definition {
	return hookrt.Definition{
		Name:           Name,
		DefaultEnabled: true,
		Registrations: []hookrt.Registration{
			{Agent: hookrt.Claude, Event: "PreToolUse", Matcher: "Bash"},
			{Agent: hookrt.Claude, Event: "PreToolUse", Matcher: "Edit|Write|MultiEdit|NotebookEdit"},
			{Agent: hookrt.Codex, Event: "PreToolUse", Matcher: "Bash", AdditionalContextLimit: contextLimit},
			{Agent: hookrt.Codex, Event: "PreToolUse", Matcher: "^(apply_patch|Edit|Write)$",
				AdditionalContextLimit: contextLimit},
			// 宣言は実行に成功した後に記録する（利用者が実行を拒んだ宣言や、失敗した宣言を残さないため）。
			{Agent: hookrt.Claude, Event: "PostToolUse", Matcher: "Bash"},
			{Agent: hookrt.Codex, Event: "PostToolUse", Matcher: "Bash", AdditionalContextLimit: contextLimit},
		},
		Gate:        gate,
		Run:         run,
		NewSettings: func() any { return &Settings{} },
	}
}

// Settings は generated-edit-guard 固有の設定である。
type Settings struct {
	Enabled *bool `yaml:"enabled"`
	// Markers は生成ファイルとみなす追加のマーカー（Go の正規表現）である。ファイルの先頭に行単位（(?m)）で当てる。
	Markers []string `yaml:"markers"`
}

// gateKeywords は一次ゲートの語である。書き込みの兆候になる語が無い入力は、設定もファイルも読まずに抜ける。
// 編集ツールの payload は file_path・notebook_path・apply_patch の見出し（File:）で通す。
// > は、エスケープした JSON の表記（\u003e）でも通す。
var gateKeywords = [][]byte{
	[]byte(">"), []byte("\\u003e"), []byte("sed"), []byte("perl"), []byte("tee"), []byte("sponge"),
	[]byte("cp"), []byte("mv"),
	[]byte("install"), []byte("truncate"), []byte("of="), []byte("patch"), []byte("apply"), []byte("python"),
	[]byte("pypy"), []byte("node"), []byte("ruby"), []byte("inplace"), []byte(DeclareCommand), []byte("file_path"),
	[]byte("notebook_path"), []byte("File:"),
}

func gate(input []byte) bool {
	for _, keyword := range gateKeywords {
		if bytes.Contains(input, keyword) {
			return true
		}
	}
	return false
}

// now は宣言の時刻である。テストで固定する。
var now = time.Now

// request は判定の材料である。
type request struct {
	session string
	cwd     string
	// event は hook_event_name で、PostToolUse のときは宣言だけを扱う。
	event string
	// succeeded は PostToolUse の tool_response が成功を示すかである。
	succeeded bool
	// paths は書き込み先の候補（絶対パス）である。
	paths []string
	// scan は Bash のコマンドの走査の結果で、Bash でなければ nil である。
	scan *bashScan
}

func run(c *hookrt.Context) error {
	req, ok := readRequest(c)
	if !ok {
		return nil
	}
	var settings Settings
	// 設定の型が違っても（install が報告する）、標準のマーカーで判定は続ける。
	_ = c.Settings(&settings)
	markers := newMarkerSet(settings.Markers)
	st, storeErr := openStore()
	usable := storeErr == nil && req.session != ""
	if req.event == "PostToolUse" {
		if req.scan != nil && req.succeeded && usable {
			recordDeclarations(c, st, req, markers)
		}
		return nil
	}
	if len(req.paths) == 0 {
		return nil
	}
	isGenerated := func(path string) bool {
		_, ok := markers.detect(path)
		return ok
	}
	var blocked []generated
	var allowed []reportItem
	for _, path := range req.paths {
		found, ok := markers.detect(path)
		if !ok || (req.scan != nil && req.scan.copiedFromGenerated(path, isGenerated)) {
			continue
		}
		if usable {
			if entry, ok := st.lookup(req.session, path); ok {
				allowed = append(allowed, reportItem{path: path, reason: entry.Reason, command: found.command})
				continue
			}
		}
		blocked = append(blocked, found)
	}
	language := c.Language()
	switch {
	case len(blocked) > 0:
		c.Deny(denyReason(language, blocked, req.cwd))
	case len(allowed) > 0:
		c.AddContext("PreToolUse", reportContext(language, nil, allowed, req.cwd))
	}
	return nil
}

// recordDeclarations は成功した Bash の宣言を記録し、受け付けたことと最終報告の指示を注入する。
// 記録できなかったパス（変数を含むもの）があれば、リテラルで宣言し直すよう伝える。
func recordDeclarations(c *hookrt.Context, st store, req request, markers markerSet) {
	var notes []reportItem
	var unresolved []string
	for _, declaration := range req.scan.declarations {
		unresolved = append(unresolved, declaration.unresolved...)
		if len(declaration.paths) == 0 || st.save(req.session, declaration.reason, declaration.paths, now()) != nil {
			continue
		}
		for _, path := range declaration.paths {
			found, _ := markers.detect(path)
			notes = append(notes, reportItem{path: path, reason: declaration.reason, command: found.command})
		}
	}
	language := c.Language()
	var parts []string
	if len(unresolved) > 0 {
		parts = append(parts, unrecordedNotice(language, unresolved))
	}
	if len(notes) > 0 {
		parts = append(parts, reportContext(language, notes, nil, req.cwd))
	}
	text := strings.Join(parts, "\n")
	if len(text) > contextLimit {
		// 両方を合わせて Codex の上限を超えるなら、最終報告の指示を優先する。
		text = parts[len(parts)-1]
	}
	if text != "" {
		c.AddContext("PostToolUse", text)
	}
}

// readRequest は payload（デバッグ経路ではコマンド文字列も）から判定の材料を読む。判定しない入力なら ok は偽になる。
func readRequest(c *hookrt.Context) (request, bool) {
	var payload map[string]json.RawMessage
	if json.Unmarshal(c.Input, &payload) != nil || payload == nil {
		if !c.FromArgs {
			return request{}, false
		}
		// デバッグ経路の引数が payload でなければ、Bash のコマンド文字列として読む。
		cwd, err := py.Getcwd()
		if err != nil {
			return request{}, false
		}
		scan := scanBash(string(c.Input), cwd)
		return request{cwd: cwd, paths: scan.paths, scan: scan}, true
	}
	var req request
	var toolName string
	_ = json.Unmarshal(payload["tool_name"], &toolName)
	_ = json.Unmarshal(payload["hook_event_name"], &req.event)
	if req.event == "PostToolUse" {
		if !bytes.Contains(c.Input, []byte(DeclareCommand)) {
			return request{}, false
		}
		decoder := json.NewDecoder(bytes.NewReader(payload["tool_response"]))
		decoder.UseNumber()
		var response any
		_ = decoder.Decode(&response)
		req.succeeded = toolresponse.Succeeded(response)
	}
	_ = json.Unmarshal(payload["session_id"], &req.session)
	_ = json.Unmarshal(payload["cwd"], &req.cwd)
	if req.cwd == "" || !filepath.IsAbs(req.cwd) {
		cwd, err := py.Getcwd()
		if err != nil {
			return request{}, false
		}
		req.cwd = cwd
	}
	var toolInput any
	if json.Unmarshal(payload["tool_input"], &toolInput) != nil {
		return request{}, false
	}
	fields, isObject := toolInput.(map[string]any)
	switch toolName {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		if !isObject {
			return request{}, false
		}
		path, _ := fields["file_path"].(string)
		if path == "" {
			path, _ = fields["notebook_path"].(string)
		}
		if path == "" {
			return request{}, false
		}
		req.paths = []string{joinUnder(req.cwd, path)}
	case "apply_patch":
		var texts []string
		collectStrings(toolInput, &texts)
		for _, text := range texts {
			for _, match := range patchFileRE.FindAllStringSubmatch(text, -1) {
				if path := joinUnder(req.cwd, match[1]); !slices.Contains(req.paths, path) {
					req.paths = append(req.paths, path)
				}
			}
		}
	default:
		// tool_name が無い・空・ほかの値なら Bash として読む。文字列でない command は判定しない。
		if !isObject {
			return request{}, false
		}
		command, _ := fields["command"].(string)
		if command == "" {
			return request{}, false
		}
		req.scan = scanBash(command, req.cwd)
		req.paths = req.scan.paths
	}
	return req, true
}

// collectStrings は JSON の値に含まれる文字列をすべて集める。
func collectStrings(value any, out *[]string) {
	switch typed := value.(type) {
	case string:
		*out = append(*out, typed)
	case []any:
		for _, item := range typed {
			collectStrings(item, out)
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			collectStrings(typed[key], out)
		}
	}
}

// displayPath は cwd の下のパスを相対で、それ以外を絶対で表示する。
func displayPath(path, cwd string) string {
	if relative, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(relative, "..") {
		return relative
	}
	return path
}
