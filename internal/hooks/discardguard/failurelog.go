package discardguard

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/HappyOnigiri/hhx/internal/hookcache"
)

// --- snapshot を作れなかったときの記録 -----------------------------------------------

// failureLogName は snapshot を作れずに deny したときの記録のファイル名である（hookcache の下）。
// 原因（git 管理下でない・タイムアウト・lock の競合など）は理由文からは分からないので、後から調べる材料として残す。
const failureLogName = "snapshot-failures.jsonl"

// maxFailureLogSize を超えた記録は 1 世代だけ退避してから書き足す。調査の材料なので古い分は失ってよい。
const maxFailureLogSize = 1 << 20

// failureEntry は記録の 1 行である。
type failureEntry struct {
	Time       string `json:"time"`
	HHXVersion string `json:"hhx_version"`
	SessionID  string `json:"session_id,omitempty"`
	CWD        string `json:"cwd"`
	Command    string `json:"command"`
	// Target は保存しようとした作業ツリー（rev-parse に失敗したときは対象のディレクトリ）である。
	Target string `json:"target"`
	gitFailure
}

// now は記録の時刻である。テストで固定する。
var now = time.Now

// recordFailure は value を記録の末尾に 1 行で書き足す。複数のプロセスが同時に書いても行が混ざらないよう、1 回の write で書く。
func recordFailure(value failureEntry) error {
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
	path := filepath.Join(dir, failureLogName)
	if info, err := os.Stat(path); err == nil && info.Size() > maxFailureLogSize {
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
