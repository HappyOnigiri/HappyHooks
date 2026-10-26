// Package permissionrequestguard は、Claude Code が Bash の実行に利用者の承認を求めた時点で、
// 承認を待たずに deny する hook（permission-request-guard）である。
//
// 背景:
//
// dangerous-rm-guard は Claude Code の組み込みの確認（bypassPermissions でも出る ask）を PreToolUse で先回りして止めるが、
// 判定を移植した版より新しい分岐や、payload から算出できない条件（追加作業ディレクトリなど）はすり抜けて確認待ちになる。
// PermissionRequest は本体が ask と決めた後に発火するので、ask の原因を解析しなくても受け止められる。
//
// 方針:
//   - コマンドは解析しない。PermissionRequest まで来た Bash はすべて deny する。
//     既知の形は PreToolUse の dangerous-rm-guard が具体的な書き換えの案内つきで先に止め、この hook は取りこぼしの受け皿になる。
//   - payload に ask の理由は入っていない（v2.1.310 で実測）。理由文は permission_mode だけで書き分ける。
//     bypassPermissions で ask になるのは主に本体の安全確認で（利用者の ask のルールでも来る）、それ以外のモードでは未承認のコマンドも含む。
//   - deny したコマンドは hookcache の下に JSONL で記録する。取りこぼしの形を後から dangerous-rm-guard の判定へ足すためである。
//     記録に失敗しても deny は出す（記録は判定の材料で、止めることの方が優先）。
//
// 制約（v2.1.310 で実測）:
//   - hook の実行中も確認画面は表示され、操作を受け付ける。その間の利用者の回答は hook より優先される。
//   - 無出力で終わると従来どおり確認画面が残る（fail-open は「確認が出る」側に倒れる）。
package permissionrequestguard

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/HappyOnigiri/hhx/internal/hookcache"
	"github.com/HappyOnigiri/hhx/internal/hookrt"
	"github.com/HappyOnigiri/hhx/internal/version"
)

// Name は hook の名前である。
const Name = "permission-request-guard"

// logName は deny したコマンドの記録のファイル名である。
const logName = "denied.jsonl"

// maxLogSize を超えた記録は 1 世代だけ退避してから書き足す。調査の材料なので古い分は失ってよい。
const maxLogSize = 1 << 20

// bypassMode は、ask が本体の安全確認からだけ来る permission_mode である。
const bypassMode = "bypassPermissions"

// Definition は permission-request-guard の定義を返す。
func Definition() hookrt.Definition {
	return hookrt.Definition{
		Name:           Name,
		DefaultEnabled: true,
		Registrations: []hookrt.Registration{
			// Codex には PermissionRequest に当たるイベントが無いので、Claude Code にだけ登録する。
			{Agent: hookrt.Claude, Event: "PermissionRequest", Matcher: "Bash"},
		},
		// 承認待ちになったときだけ発火するので、一次ゲートで軽くする必要が無い。
		Run: run,
	}
}

// request は PermissionRequest の payload のうち、判定と記録に使う項目である。
type request struct {
	SessionID      string `json:"session_id"`
	CWD            string `json:"cwd"`
	PermissionMode string `json:"permission_mode"`
	ToolName       string `json:"tool_name"`
	AgentID        string `json:"agent_id"`
	AgentType      string `json:"agent_type"`
	ToolInput      struct {
		Command any `json:"command"`
	} `json:"tool_input"`
	PermissionSuggestions json.RawMessage `json:"permission_suggestions"`
}

// entry は記録の 1 行である。dangerous-rm-guard の判定を再現するのに要る項目（コマンド・cwd・モード）と、
// 本体の判定の版を後から突き合わせるための時刻と hhx の版を残す。
// permission_suggestions はそのまま残す。default モードでは、未承認のコマンドには許可の候補が付き、
// 安全確認による ask では空だった（v2.1.310 で各 1 件を実測）ので、記録を絞り込む手がかりになる。判定には使わない。
type entry struct {
	Time           string `json:"time"`
	HHXVersion     string `json:"hhx_version"`
	SessionID      string `json:"session_id,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	AgentType      string `json:"agent_type,omitempty"`
	PermissionMode string `json:"permission_mode"`
	CWD            string `json:"cwd"`
	Command        string `json:"command"`
	// Suggestions は payload に無ければ書かない。
	Suggestions json.RawMessage `json:"permission_suggestions,omitempty"`
}

// now は記録の時刻である。テストで固定する。
var now = time.Now

func run(c *hookrt.Context) error {
	if c.FromArgs {
		// デバッグ経路（`hhx hook permission-request-guard '<コマンド>'`）では理由文の確認だけをする。
		// モードは不明なので未承認の側の理由文を出し、記録はしない。
		c.DenyPermission(reason(c.Language(), ""))
		return nil
	}
	var payload request
	if err := json.Unmarshal(c.Input, &payload); err != nil {
		return err
	}
	command, ok := payload.ToolInput.Command.(string)
	if payload.ToolName != "Bash" || !ok {
		return nil
	}
	_ = record(entry{
		Time:           now().UTC().Format(time.RFC3339),
		HHXVersion:     version.String(),
		SessionID:      payload.SessionID,
		AgentID:        payload.AgentID,
		AgentType:      payload.AgentType,
		PermissionMode: payload.PermissionMode,
		CWD:            payload.CWD,
		Command:        command,
		Suggestions:    payload.PermissionSuggestions,
	})
	c.DenyPermission(reason(c.Language(), payload.PermissionMode))
	return nil
}

// record は value を記録の末尾に 1 行で書き足す。複数のプロセスが同時に書いても行が混ざらないよう、1 回の write で書く。
func record(value entry) error {
	dir, err := hookcache.Dir(Name)
	if err != nil {
		return err
	}
	if err := hookcache.MkdirAll(dir); err != nil {
		return err
	}
	// コマンドの `<>&` をそのまま読めるよう、HTML 向けのエスケープを切る。Encode は末尾に改行を付ける。
	var line bytes.Buffer
	encoder := json.NewEncoder(&line)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	path := filepath.Join(dir, logName)
	if info, err := os.Stat(path); err == nil && info.Size() > maxLogSize {
		_ = os.Rename(path, path+".1")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(line.Bytes())
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
