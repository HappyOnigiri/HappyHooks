package discardguard

import (
	"os"
	"regexp"
	"strconv"
	"strings"

	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

// --- scratchpad ------------------------------------------------------------

// scratchRoot は Claude Code が scratchpad を置く一時ディレクトリの根である（TMPDIR に依らず /tmp/claude-<uid> の下に作る）。
// テストで差し替えるために変数にしている。
var scratchRoot = "/tmp"

// scratchpad はセッションの scratchpad（<scratchRoot>/claude-<uid>/<cwd のスラッグ>/<session_id>/scratchpad）である。
// 零値はどのパスも含まない。
type scratchpad struct {
	session string
}

// sessionIDRE は scratchpad の判定に使う session_id の形である。パスの区切りや . / .. を含む値は使わない。
var sessionIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// newScratchpad は payload の session_id のセッションの scratchpad を返す。判定は Claude Code の scratchpad のパスの形で行うので、
// Codex のセッションでは零値にする（Codex の作業用ディレクトリの形は確かめていない）。
func newScratchpad(sessionID, transcriptPath string) scratchpad {
	if !sessionIDRE.MatchString(sessionID) || isCodex(sessionID, transcriptPath) {
		return scratchpad{}
	}
	return scratchpad{session: sessionID}
}

// isCodex は transcript の名前（rollout-*-<session_id>.jsonl）で Codex かを判定する。環境変数は親の CLI の値を継承し得るため使わない。
func isCodex(sessionID, transcriptPath string) bool {
	name := transcriptPath[strings.LastIndex(transcriptPath, "/")+1:]
	return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, "-"+sessionID+".jsonl")
}

// contains は path が scratchpad の中かを返す。symlink（macOS の /tmp → /private/tmp を含む）を解決してから比べるので、
// scratchpad の中の symlink が外のリポジトリを指していれば外として扱う。<cwd のスラッグ> は 1 階層の任意の名前として扱う。
func (s scratchpad) contains(path string) bool {
	if s.session == "" {
		return false
	}
	root, err := py.Realpath(scratchRoot)
	if err != nil {
		return false
	}
	resolved, err := py.Realpath(path)
	if err != nil {
		return false
	}
	rest, ok := strings.CutPrefix(resolved, strings.TrimSuffix(root, "/")+"/claude-"+strconv.Itoa(os.Getuid())+"/")
	if !ok {
		return false
	}
	parts := strings.SplitN(rest, "/", 4)
	return len(parts) >= 3 && parts[0] != "" && parts[1] == s.session && parts[2] == "scratchpad"
}
