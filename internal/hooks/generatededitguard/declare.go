package generatededitguard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/HappyOnigiri/hhx/internal/hookcache"
)

// DeclareCommand は宣言の hhx のサブコマンド名である。hook はこの名前の Bash 呼び出しを PreToolUse で見つけて記録する。
// session_id は hook の payload にしか無く、CLI からは分からないので、記録は hook が行い、CLI は形を確かめるだけにする。
const DeclareCommand = "allow-generated-edit"

// 宣言の形の誤りである。CLI が使い方の文面を選ぶために使う。
var (
	ErrNoReason = errors.New("--reason is required")
	ErrNoPaths  = errors.New("at least one path is required")
)

// UnknownOptionError は宣言に知らないオプションがあったことを表す。
type UnknownOptionError struct {
	Option string
}

func (e UnknownOptionError) Error() string {
	return "unknown option: " + e.Option
}

// Declaration は宣言の中身である。
type Declaration struct {
	// Reason はユーザーの指示の要旨である。
	Reason string
	Paths  []string
}

// ParseDeclaration は `hhx allow-generated-edit` より後ろの引数を読む。
// 形は `--reason <text>`（または `--reason=<text>`）と 1 つ以上のパスで、`--` より後ろはすべてパスとする。
func ParseDeclaration(args []string) (Declaration, error) {
	reason, indexes, err := parseDeclaration(args)
	if err != nil {
		return Declaration{}, err
	}
	declaration := Declaration{Reason: reason}
	for _, index := range indexes {
		declaration.Paths = append(declaration.Paths, args[index])
	}
	return declaration, nil
}

// parseDeclaration は ParseDeclaration の本体で、パスを args の位置で返す（hook が語の情報を引き継ぐため）。
func parseDeclaration(args []string) (reason string, paths []int, err error) {
	for index := 0; index < len(args); index++ {
		text := args[index]
		switch {
		case text == "--":
			for rest := index + 1; rest < len(args); rest++ {
				paths = append(paths, rest)
			}
			index = len(args)
		case text == "--reason":
			if index+1 < len(args) {
				index++
				reason = args[index]
			}
		case strings.HasPrefix(text, "--reason="):
			reason = text[len("--reason="):]
		case strings.HasPrefix(text, "-") && text != "-":
			return "", nil, UnknownOptionError{Option: text}
		default:
			paths = append(paths, index)
		}
	}
	if strings.TrimSpace(reason) == "" {
		return "", nil, ErrNoReason
	}
	if len(paths) == 0 {
		return "", nil, ErrNoPaths
	}
	return reason, paths, nil
}

// 宣言はセッションごとのディレクトリに、パスごとの JSON ファイルで置く。
// 1 回の宣言は新しいファイルを書くだけで、既存のファイルを読み書きしないので、並行した hook の間でロックは要らない。
// 古いセッションのディレクトリは、宣言を書くときに stateTTL を過ぎたものから消す。
const stateTTL = 30 * 24 * time.Hour

// record は宣言 1 つ分の記録である。
type record struct {
	Path       string `json:"path"`
	Reason     string `json:"reason"`
	DeclaredAt string `json:"declared_at"`
}

// store は宣言の置き場所である。
type store struct {
	root string
}

func openStore() (store, error) {
	root, err := hookcache.Dir(Name)
	return store{root: root}, err
}

func hashKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

func (s store) sessionDir(session string) string {
	return filepath.Join(s.root, hashKey(session))
}

// canonical はパスを比べる形にする。実在すれば symlink を解決する（宣言と編集で表記が違っても同じファイルとみなす）。
func canonical(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// save は session の宣言として paths を記録する。
func (s store) save(session, reason string, paths []string, now time.Time) error {
	s.prune(now)
	dir := s.sessionDir(session)
	for _, path := range paths {
		data, err := json.Marshal(record{Path: canonical(path), Reason: reason, DeclaredAt: now.UTC().Format(time.RFC3339)})
		if err != nil {
			return err
		}
		if err := hookcache.WriteFile(dir, hashKey(canonical(path))+".json", data); err != nil {
			return err
		}
	}
	return nil
}

// lookup は session で path が宣言済みなら、その記録を返す。
func (s store) lookup(session, path string) (record, bool) {
	key := canonical(path)
	data, err := os.ReadFile(filepath.Join(s.sessionDir(session), hashKey(key)+".json"))
	if err != nil {
		return record{}, false
	}
	var found record
	if json.Unmarshal(data, &found) != nil || found.Path != key {
		return record{}, false
	}
	return found, true
}

// prune は stateTTL より前に更新したセッションのディレクトリを消す。失敗しても宣言の記録は続ける。
func (s store) prune(now time.Time) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !entry.IsDir() || now.Sub(info.ModTime()) < stateTTL {
			continue
		}
		_ = os.RemoveAll(filepath.Join(s.root, entry.Name()))
	}
}
