// Package agentslocalcontext は、Codex にディレクトリ固有の AGENTS.local.md を追加で注入する hook（agents-local-context）である。
//
// SessionStart と PreToolUse で、対象のパスに適用されるファイルを Git のルートから順に探し、
// 同じセッションへ未注入か内容が変わったものだけを developer context として返す。
// SessionStart は compact なら記録済みのルールを読み直して注入し、記録をその内容で置き換える。
// それ以外の SessionStart（startup・resume・clear）は、PreToolUse と同じく cwd のルールを記録して注入する。
// SubagentStart では記録済みのルールを毎回すべて注入する（記録は変えない）。
// SessionStart・PreToolUse・SubagentStart 以外のイベント（SessionEnd など）では何もせず、記録も消さない。
//
// 警告は systemMessage で知らせるが、判断のフィールドは返さず、Codex のツールの実行やセッションの進行を止めない。
// 一次ゲートは無く、全呼び出しで判定する。デバッグ経路の引数は stdin の代わりの payload（JSON）として読む。
//
// 状態は `~/.cache/hhx/agents-local-context/` に、セッションごとの JSON ファイル（ルールのパス → digest と記録時刻）で持つ。
// 同じディレクトリのロック用ファイルへの flock で排他し、読み・判定・書き込みを 1 回のロックの中で行う。
// 移植元は SQLite（`$CODEX_HOME/hook-state/agents-local-context.sqlite3`）に置いていた。
// 単一バイナリに SQLite のドライバを入れると全 hook の大きさと読み込みの費用が増える一方、
// 必要なのは「同じセッションで同じ内容を二重に注入しない」排他だけで、ファイルのロックで同じ保証が得られる。
package agentslocalcontext

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HappyOnigiri/hhx/internal/hookcache"
	"github.com/HappyOnigiri/hhx/internal/hookexec"
	"github.com/HappyOnigiri/hhx/internal/hookrt"
	py "github.com/HappyOnigiri/hhx/internal/pycompat"
	"github.com/HappyOnigiri/hhx/internal/toolresponse"
)

// Name は hook の名前である。
const Name = "agents-local-context"

const (
	ruleName = "AGENTS.local.md"
	// maxContextBytes は注入の合計の上限である。Codex の additionalContextLimit（登録の値）と同じにする。
	maxContextBytes = 32 * 1024
	stateTTL        = 30 * 24 * time.Hour
	gitTimeout      = 5 * time.Second
	// lockTimeout はロックを待つ上限である。移植元の SQLite の busy_timeout と同じ 3 秒にする。
	lockTimeout = 3 * time.Second
)

// Definition は agents-local-context の定義を返す。Codex にだけ登録する（Claude Code は AGENTS.local.md を読まない）。
func Definition() hookrt.Definition {
	registration := func(event, matcher string) hookrt.Registration {
		return hookrt.Registration{
			Agent: hookrt.Codex, Event: event, Matcher: matcher, Timeout: 10,
			AdditionalContextLimit: maxContextBytes,
		}
	}
	return hookrt.Definition{
		Name:           Name,
		DefaultEnabled: true,
		// 移植元の登録をそのまま写す。SessionStart は matcher なしと ^compact$ の 2 つのグループに登録していた。
		Registrations: []hookrt.Registration{
			registration("PreToolUse", ""),
			registration("SessionStart", ""),
			registration("SessionStart", "^compact$"),
			registration("SubagentStart", ""),
		},
		Run: run,
	}
}

// pathKeys はパスとして読む tool_input の項目名である。名前に path・file・director を含む項目も読む。
var pathKeys = map[string]bool{
	"directory": true, "dir": true, "file": true, "file_path": true, "filename": true, "move_path": true,
	"path": true, "paths": true, "target": true, "targets": true, "workdir": true, "working_directory": true,
}

// commandKeys はコマンド文字列として読む tool_input の項目名である。
var commandKeys = []string{"cmd", "command"}

var (
	// patchPathRE は apply_patch の対象のファイルの行（*** Add/Delete/Update File: と ---・+++）である。
	patchPathRE = regexp.MustCompile(`(?m)^(?:\*\*\* (?:Add|Delete|Update) File:|---|\+\+\+)` + py.Space + `+(.+?)` +
		py.Space + `*$`)
	// lineSuffixRE は行番号の接尾辞（:12 や :12:3）である。Python の $ は末尾の改行の直前にも一致するので、改行は残す。
	lineSuffixRE    = regexp.MustCompile(`:` + py.Digit + `+(?::` + py.Digit + `+)?(\n?)$`)
	sessionUnsafeRE = regexp.MustCompile(`[^A-Za-z0-9_-]`)
)

// rule は注入する AGENTS.local.md 1 つである。
type rule struct {
	path, scope, digest, text string
}

func (r rule) context() string {
	return "<agents-local-instructions>\n" +
		"source: " + r.path + "\n" +
		"scope: " + r.scope + "/**\n" +
		"<INSTRUCTIONS>\n" +
		py.RStrip(r.text) + "\n" +
		"</INSTRUCTIONS>\n" +
		"</agents-local-instructions>"
}

func run(c *hookrt.Context) error {
	t := texts{language: c.Language()}
	event, rules, warnings := handle(c.Input, t)
	contexts := make([]string, len(rules))
	for index, rule := range rules {
		contexts[index] = rule.context()
	}
	c.Notify(event, strings.Join(contexts, "\n\n"), warningText(t, warnings))
	return nil
}

// handle は payload を読んでイベントごとに処理し、出力のイベント名・注入するルール・警告を返す。
func handle(input []byte, t texts) (string, []rule, []string) {
	if !utf8.Valid(input) {
		return "PreToolUse", nil, []string{t.warn(idInvalidInput, map[string]any{"Error": "stdin is not valid UTF-8"})}
	}
	value, err := decodeJSON(string(input))
	if err != nil {
		return "PreToolUse", nil, []string{t.warn(idInvalidInput, map[string]any{"Error": err.Error()})}
	}
	payload, ok := value.(map[string]any)
	if !ok {
		return "PreToolUse", nil, []string{t.warn(idNotObject, nil)}
	}
	event, _ := payload["hook_event_name"].(string)
	if event == "" {
		event = "PreToolUse"
	}
	sessionID, _ := payload["session_id"].(string)
	state := newStateStore(t)
	resolver := &gitRoots{cache: map[string]gitRootResult{}}
	claim := func() (string, []rule, []string) {
		rules, warnings, err := applicableRules(payload, resolver, t)
		if err != nil {
			return "PreToolUse", nil, []string{t.warn(idUnexpected, map[string]any{"Kind": "OSError", "Error": err.Error()})}
		}
		claimed, claimWarnings := state.claim(sessionID, rules)
		return event, claimed, append(warnings, claimWarnings...)
	}
	switch event {
	case "PreToolUse":
		return claim()
	case "SessionStart":
		if payload["source"] == "compact" {
			rules, warnings := state.restored(sessionID)
			return event, rules, append(warnings, state.replace(sessionID, rules)...)
		}
		return claim()
	case "SubagentStart":
		rules, warnings := state.restored(sessionID)
		return event, rules, warnings
	}
	return event, nil, nil
}

// decodeJSON は text 全体を 1 つの JSON の値として読む。数値は json.Number のまま持つ。
func decodeJSON(text string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	// 値の後ろに空白以外が残っていれば、Python の json.loads と同じく壊れているとみなす。
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("extra data after the JSON value")
	}
	return value, nil
}

// warningText は警告を 1 行にまとめる。最大 3 件を出し、残りは件数だけ書く。
func warningText(t texts, warnings []string) string {
	var messages []string
	for _, warning := range warnings {
		if py.Strip(warning) != "" {
			messages = append(messages, strings.Join(py.Fields(warning), " "))
		}
	}
	if len(messages) == 0 {
		return ""
	}
	shown := messages[:min(len(messages), 3)]
	if len(messages) > len(shown) {
		shown = append(shown, t.warn(idMore, map[string]any{"Count": len(messages) - len(shown)}))
	}
	return warningPrefix + strings.Join(shown, "; ")
}

// fitContext は上限に収まる順にルールを選ぶ。収まらないものは警告に回す。区切りの空行は 2 バイトに数える。
func fitContext(rules []rule, t texts) ([]rule, []string) {
	var selected []rule
	var warnings []string
	used := 0
	for _, rule := range rules {
		size := len(rule.context())
		separator := 0
		if len(selected) > 0 {
			separator = 2
		}
		if used+separator+size > maxContextBytes {
			warnings = append(warnings, t.warn(idContextLimit, map[string]any{"Limit": maxContextBytes, "Path": rule.path}))
			continue
		}
		selected = append(selected, rule)
		used += separator + size
	}
	return selected, warnings
}

// --- 対象のパス ------------------------------------------------------------------

// pathValue はパスの候補である。explicit はパスの項目として渡されたもの（コマンドから拾ったものは偽）である。
type pathValue struct {
	value    string
	explicit bool
}

// targetPaths は payload から対象のパスを集める。
func targetPaths(payload map[string]any, t texts) ([]string, []string, error) {
	cwdValue, ok := payload["cwd"].(string)
	if !ok || cwdValue == "" {
		return nil, []string{t.warn(idNoCwd, nil)}, nil
	}
	cwd, err := py.Abspath(py.Expanduser(cwdValue))
	if err != nil {
		return nil, nil, err
	}
	base := cwd
	toolInput := payload["tool_input"]
	fields, isObject := toolInput.(map[string]any)
	if isObject {
		workdir := fields["workdir"]
		if !toolresponse.Truthy(workdir) {
			workdir = fields["working_directory"]
		}
		if text, ok := workdir.(string); ok {
			if normalized, ok := normalizePath(text, cwd, true); ok {
				base = normalized
			}
		}
	}
	values := []pathValue{{base, true}}
	if isObject {
		values = append(values, nestedPathValues(fields, "")...)
		for _, key := range commandKeys {
			if command, ok := fields[key].(string); ok {
				values = append(values, commandPathValues(command)...)
			}
		}
	} else if command, ok := toolInput.(string); ok {
		values = append(values, commandPathValues(command)...)
	}
	var paths []string
	seen := map[string]bool{}
	for _, value := range values {
		if path, ok := normalizePath(value.value, base, value.explicit); ok && !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths, nil, nil
}

// nestedPathValues は tool_input の中のパスの項目の文字列を集める。コマンドの項目の文字列はここでは読まない。
func nestedPathValues(value any, key string) []pathValue {
	var found []pathValue
	switch typed := value.(type) {
	case map[string]any:
		for childKey, child := range typed {
			normalized := py.Lower(childKey)
			if _, isString := child.(string); isString && (normalized == "cmd" || normalized == "command") {
				continue
			}
			found = append(found, nestedPathValues(child, normalized)...)
		}
	case []any:
		for _, child := range typed {
			found = append(found, nestedPathValues(child, key)...)
		}
	case string:
		if pathKeys[key] || strings.Contains(key, "path") || strings.Contains(key, "file") ||
			strings.Contains(key, "director") {
			found = append(found, pathValue{typed, true})
		}
	}
	return found
}

// commandPathValues はコマンド文字列からパスの候補を best-effort で取り出す。
// apply_patch の対象の行と、Python の shlex.split と同じ分割のトークンである。引用が閉じなければトークンは足さない。
func commandPathValues(command string) []pathValue {
	var values []pathValue
	for _, match := range patchPathRE.FindAllStringSubmatch(command, -1) {
		values = append(values, pathValue{match[1], false})
	}
	if tokens, err := py.ShlexSplit(command, " \t\r\n", ""); err == nil {
		for _, token := range tokens {
			values = append(values, pathValue{token, false})
		}
	}
	return values
}

// normalizePath は候補を絶対パスにする。パスとして扱わないものは ok が偽になる。
// 引用符を外し、--opt=value の値を取り、行番号の接尾辞を外し、~ と環境変数を展開し、glob の前で切る。
// 明示されていない（コマンド由来の）値は、存在しないうえ / を含まず . で始まらなければ捨てる。
func normalizePath(value, base string, explicit bool) (string, bool) {
	text := strings.Trim(py.Strip(value), `'"`)
	if text == "" || text == "-" || strings.Contains(text, "://") {
		return "", false
	}
	switch {
	case strings.HasPrefix(text, "-") && strings.Contains(text, "="):
		text = text[strings.Index(text, "=")+1:]
	case strings.HasPrefix(text, "-") && !explicit:
		return "", false
	}
	text = lineSuffixRE.ReplaceAllString(text, "$1")
	text = py.Expandvars(py.Expanduser(text))
	if index := strings.IndexAny(text, "*?["); index >= 0 {
		if text = strings.TrimRight(text[:index], "/"); text == "" {
			text = "."
		}
	}
	if text == "" {
		return "", false
	}
	path := py.Normpath(py.Join(base, text))
	if !explicit && !exists(path) && !strings.Contains(value, "/") && !strings.HasPrefix(value, ".") {
		return "", false
	}
	return path, true
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// parent は pathlib の PurePosixPath.parent である。ルート（/ と //）の親はルート自身である。
func parent(path string) string {
	root := ""
	switch {
	case strings.HasPrefix(path, "//") && !strings.HasPrefix(path, "///"):
		root = "//"
	case strings.HasPrefix(path, "/"):
		root = "/"
	}
	rest := strings.TrimRight(path[len(root):], "/")
	index := strings.LastIndex(rest, "/")
	if index < 0 {
		if root == "" {
			return "."
		}
		return root
	}
	return root + rest[:index]
}

// --- ルールの探索 ----------------------------------------------------------------

type gitRootResult struct {
	root string
	ok   bool
}

// gitRoots は 1 回の実行の中で、ディレクトリごとの Git のルートを覚える。全 PreToolUse で走るので git を繰り返さない。
type gitRoots struct {
	cache map[string]gitRootResult
}

func (g *gitRoots) root(dir string) (string, bool) {
	if result, ok := g.cache[dir]; ok {
		return result.root, result.ok
	}
	result := gitRootResult{}
	if output, ok := hookexec.Output("", gitTimeout, "git", "-C", dir, "rev-parse", "--show-toplevel"); ok {
		if root := py.Strip(output); root != "" {
			if resolved, err := py.Realpath(root); err == nil {
				result = gitRootResult{resolved, true}
			}
		}
	}
	g.cache[dir] = result
	return result.root, result.ok
}

// nearestExistingDirectory は target（ディレクトリでなければその親）から、存在するディレクトリまで遡る。
func nearestExistingDirectory(target string) (string, bool) {
	candidate := target
	if !isDir(target) {
		candidate = parent(target)
	}
	for !exists(candidate) {
		next := parent(candidate)
		if next == candidate {
			return "", false
		}
		candidate = next
	}
	if isDir(candidate) {
		return candidate, true
	}
	return parent(candidate), true
}

// rulePathsForTarget は target に適用される AGENTS.local.md の候補を、Git のルートから target の階層まで返す。
func rulePathsForTarget(target string, roots *gitRoots) ([]string, error) {
	scope, ok := nearestExistingDirectory(target)
	if !ok {
		return nil, nil
	}
	root, ok := roots.root(scope)
	if !ok {
		return nil, nil
	}
	scope, err := py.Realpath(scope)
	if err != nil {
		return nil, err
	}
	var relative []string
	switch {
	case scope == root:
	case root == "/" && strings.HasPrefix(scope, "/"):
		relative = strings.Split(strings.Trim(scope, "/"), "/")
	case strings.HasPrefix(scope, root+"/"):
		relative = strings.Split(scope[len(root)+1:], "/")
	default:
		return nil, nil
	}
	directories := []string{root}
	current := root
	for _, part := range relative {
		current = py.Join(current, part)
		directories = append(directories, current)
	}
	paths := make([]string, len(directories))
	for index, directory := range directories {
		paths[index] = py.Join(directory, ruleName)
	}
	return paths, nil
}

// partCount は pathlib の len(path.parts) である（絶対パスはルートも 1 つに数える）。
func partCount(path string) int {
	count := 0
	if strings.HasPrefix(path, "/") {
		count++
	}
	for _, part := range strings.Split(path, "/") {
		if part != "" {
			count++
		}
	}
	return count
}

// applicableRules は payload の対象に適用されるルールを、浅い順（同じ深さはバイト列の順）に読む。
func applicableRules(payload map[string]any, roots *gitRoots, t texts) ([]rule, []string, error) {
	targets, warnings, err := targetPaths(payload, t)
	if err != nil {
		return nil, nil, err
	}
	unique := map[string]bool{}
	for _, target := range targets {
		paths, err := rulePathsForTarget(target, roots)
		if err != nil {
			return nil, nil, err
		}
		for _, path := range paths {
			unique[path] = true
		}
	}
	ordered := make([]string, 0, len(unique))
	for path := range unique {
		ordered = append(ordered, path)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if left, right := partCount(ordered[i]), partCount(ordered[j]); left != right {
			return left < right
		}
		return ordered[i] < ordered[j]
	})
	var rules []rule
	for _, path := range ordered {
		rule, found, warning := readRule(path, t)
		switch {
		case warning != "":
			warnings = append(warnings, warning)
		case found:
			rules = append(rules, rule)
		}
	}
	return rules, warnings, nil
}

// readRule は path のルールを読む。通常のファイルでなければ found は偽になり、読めなければ警告を返す。
func readRule(path string, t texts) (rule, bool, string) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return rule{}, false, ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return rule{}, false, t.warn(idReadRule, map[string]any{"Path": path, "Error": err.Error()})
	}
	if !utf8.Valid(data) {
		return rule{}, false, t.warn(idReadRuleNotUTF8, map[string]any{"Path": path})
	}
	sum := sha256.Sum256(data)
	return rule{path: path, scope: parent(path), digest: hex.EncodeToString(sum[:]), text: string(data)}, true, ""
}

// --- 状態 ------------------------------------------------------------------------

// stateStore はセッションごとの注入済みの記録である。
type stateStore struct {
	dir   string
	err   error
	texts texts
}

type stateFile struct {
	Rules map[string]stateEntry `json:"rules"`
}

type stateEntry struct {
	Digest   string `json:"digest"`
	LoadedAt int64  `json:"loaded_at"`
}

func newStateStore(t texts) *stateStore {
	dir, err := hookcache.Dir(Name)
	return &stateStore{dir: dir, err: err, texts: t}
}

// fileName はセッションの記録のファイル名である。安全な文字に絞った session_id に、元の値のハッシュを足して衝突させない。
func fileName(sessionID string) string {
	safe := sessionUnsafeRE.ReplaceAllString(sessionID, "")
	sum := sha256.Sum256([]byte(sessionID))
	return safe[:min(len(safe), 64)] + "-" + hex.EncodeToString(sum[:])[:16] + ".json"
}

func (s *stateStore) ready() error {
	if s.err != nil {
		return s.err
	}
	return hookcache.MkdirAll(s.dir)
}

func (s *stateStore) load(sessionID string) (stateFile, error) {
	state := stateFile{Rules: map[string]stateEntry{}}
	data, err := os.ReadFile(filepath.Join(s.dir, fileName(sessionID)))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	// 壊れた記録は無かったものとして扱い、次の書き込みで置き換える。
	if json.Unmarshal(data, &state) != nil || state.Rules == nil {
		state.Rules = map[string]stateEntry{}
	}
	return state, nil
}

func (s *stateStore) save(sessionID string, state stateFile) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := hookcache.WriteFile(s.dir, fileName(sessionID), data); err != nil {
		return err
	}
	s.pruneSessions(sessionID)
	return nil
}

// pruneSessions は 30 日より前に更新された他のセッションの記録を消す。書き込みのついでに、ロックの中で呼ぶ。
func (s *stateStore) pruneSessions(sessionID string) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	current := fileName(sessionID)
	for _, entry := range entries {
		if entry.Name() == current || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > stateTTL {
			_ = os.Remove(filepath.Join(s.dir, entry.Name()))
		}
	}
}

// withLock はロック用のファイルへの flock を取って f を実行する。
func (s *stateStore) withLock(f func() error) error {
	if err := s.ready(); err != nil {
		return err
	}
	unlock, err := lockFile(filepath.Join(s.dir, ".lock"), lockTimeout)
	if err != nil {
		return err
	}
	defer unlock()
	return f()
}

// claim は未注入か更新されたルールを、排他を取って確保する。
func (s *stateStore) claim(sessionID string, rules []rule) ([]rule, []string) {
	if len(rules) == 0 {
		return nil, nil
	}
	if sessionID == "" {
		selected, warnings := fitContext(rules, s.texts)
		return selected, append(warnings, s.texts.warn(idNoSessionClaim, nil))
	}
	var claimed []rule
	var warnings []string
	err := s.withLock(func() error {
		state, err := s.load(sessionID)
		if err != nil {
			return err
		}
		now := time.Now().Unix()
		changed := len(state.Rules)
		for path, entry := range state.Rules {
			if entry.LoadedAt < now-int64(stateTTL/time.Second) {
				delete(state.Rules, path)
			}
		}
		changed -= len(state.Rules)
		var pending []rule
		for _, rule := range rules {
			if entry, ok := state.Rules[rule.path]; ok && entry.Digest == rule.digest {
				continue
			}
			pending = append(pending, rule)
		}
		claimed, warnings = fitContext(pending, s.texts)
		for _, rule := range claimed {
			state.Rules[rule.path] = stateEntry{Digest: rule.digest, LoadedAt: now}
		}
		if len(claimed) == 0 && changed == 0 {
			return nil
		}
		return s.save(sessionID, state)
	})
	if err != nil {
		selected, warnings := fitContext(rules, s.texts)
		return selected, append(warnings, s.texts.warn(idStateClaim, map[string]any{"Error": err.Error()}))
	}
	return claimed, warnings
}

// restored は記録済みのルールを読み直す。
func (s *stateStore) restored(sessionID string) ([]rule, []string) {
	if sessionID == "" {
		return nil, []string{s.texts.warn(idNoSessionStored, nil)}
	}
	var paths []string
	err := s.ready()
	if err == nil {
		var state stateFile
		if state, err = s.load(sessionID); err == nil {
			for path := range state.Rules {
				paths = append(paths, path)
			}
		}
	}
	if err != nil {
		return nil, []string{s.texts.warn(idStateStored, map[string]any{"Error": err.Error()})}
	}
	sort.Strings(paths)
	var rules []rule
	var warnings []string
	for _, path := range paths {
		rule, found, warning := readRule(path, s.texts)
		switch {
		case warning != "":
			warnings = append(warnings, warning)
		case found:
			rules = append(rules, rule)
		}
	}
	rules, limitWarnings := fitContext(rules, s.texts)
	return rules, append(warnings, limitWarnings...)
}

// replace はセッションの記録を rules で置き換える（compact の後）。
func (s *stateStore) replace(sessionID string, rules []rule) []string {
	if sessionID == "" {
		return nil
	}
	err := s.withLock(func() error {
		state := stateFile{Rules: map[string]stateEntry{}}
		now := time.Now().Unix()
		for _, rule := range rules {
			state.Rules[rule.path] = stateEntry{Digest: rule.digest, LoadedAt: now}
		}
		return s.save(sessionID, state)
	})
	if err != nil {
		return []string{s.texts.warn(idStateReplace, map[string]any{"Error": err.Error()})}
	}
	return nil
}
