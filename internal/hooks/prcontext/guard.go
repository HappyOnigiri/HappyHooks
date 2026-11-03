// Package prcontext は、プロンプト中の GitHub の PR の状況をコンテキストへ注入する hook（pr-context）である。
//
// UserPromptSubmit で動き、stdout の平文がそのままコンテキストに入る。狙いは次の 3 つだけで、往復を減らすこと自体は目的にしない。
//  1. コンテキストを圧迫しない
//  2. 誤った操作を防ぐ（マージ済みの PR へ push する、base を main と決め打ちする、など）
//  3. 勘違いによる無駄な調査を防ぐ（ローカルの main を読んで「実装が無い」と誤答する、など）。
//     ローカルの作業ツリーは PR ではないことを毎回明示する。
//
// 方針:
//   - 対象は URL の形（github.com/<owner>/<repo>/pull/<n>）だけで、`#10088` のような番号だけのものは拾わない。
//   - PR 本文は入れない。mergeable は CONFLICTING のときだけ出す（mergeStateStatus の BLOCKED はレビュー待ちでも付く）。
//   - 同じセッションで内容が変わっていなければ再注入しない（注入済みの記録）。
//   - 何が起きても無出力で終える。プロンプトは止めない。
//
// workspace の扱い: cwd が Git 管理外で、直下に Git のリポジトリが 2 つ以上あるときは、origin が一致する clone を探して
// gh の実行先とローカルの状態の確認先にする。一致する clone が無ければ、認証のための実行先にだけ先頭の clone を使い、
// そのリポジトリのローカルの状態としては扱わない。
//
// 既知の限界: ホスト名の直前が区切りなら拾うので、`evil.com/github.com/o/r/pull/1` にも反応する。
//
// デバッグ経路（`hhx hook pr-context '<プロンプト>'`）の引数はプロンプトで、cwd はプロセスの作業ディレクトリである。
// session_id を持たないので、毎回そのまま出力する。
//
// 移植元（Python）で例外になって無出力で終わっていた入力は無出力にする。gh の応答の型が想定と違うとき
// （文字列でない headRefOid など）は、移植元と同じくその PR だけを飛ばすか、全体を無出力にする。
// 再現していない違い: 配列の headRefOid（Python は切り出して表示する）は、その PR を飛ばす。
package prcontext

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/HappyOnigiri/hhx/internal/hookcache"
	"github.com/HappyOnigiri/hhx/internal/hookexec"
	"github.com/HappyOnigiri/hhx/internal/hookrt"
	"github.com/HappyOnigiri/hhx/internal/i18n"
	py "github.com/HappyOnigiri/hhx/internal/pycompat"
	"github.com/HappyOnigiri/hhx/internal/toolresponse"
)

// Name は hook の名前である。
const Name = "pr-context"

// Definition は pr-context の定義を返す。
func Definition() hookrt.Definition {
	return hookrt.Definition{
		Name:           Name,
		DefaultEnabled: true,
		Registrations: []hookrt.Registration{
			{Agent: hookrt.Claude, Event: "UserPromptSubmit", Timeout: 15, StatusMessage: "Fetching PR context..."},
			{Agent: hookrt.Codex, Event: "UserPromptSubmit", Timeout: 15, StatusMessage: "Fetching PR context..."},
		},
		Gate: gate,
		Run:  run,
	}
}

const (
	// maxPRs と maxComments は 1 プロンプトあたりの上限である（大量の貼り付け対策）。
	maxPRs      = 3
	maxComments = 2
	// cacheTTL は取得のキャッシュの寿命、sessionTTL は注入済みの記録の寿命である。
	cacheTTL   = 90 * time.Second
	sessionTTL = 24 * time.Hour
	gitTimeout = 5 * time.Second
)

// ghTimeout は gh の 1 回の上限である。登録の timeout（15 秒）より短くし、全件を並列に取るので 1 回分で収まる。
var ghTimeout = 8 * time.Second

// ghFields は gh pr view で取る項目である。CI の状態とレビューの状況は、必要なら都度 gh を叩けばよいので取らない。
const ghFields = "number,title,state,isDraft,headRefName,headRefOid,baseRefName," +
	"mergeable,additions,deletions,changedFiles,mergeCommit"

// urlRE は PR の URL である。ホスト名は境界付きで見る（notgithub.com/... のような別のホストを拾わないため）。
// Python の \w と \d は Unicode の文字を含むので、pycompat の文字クラスで書く。
var urlRE = regexp.MustCompile(`(?:^|[^` + py.WordChars + `.-])(?:www\.)?github\.com/` +
	`([A-Za-z0-9._-]+/[A-Za-z0-9._-]+)/pull/(` + py.Digit + `+)(?:#discussion_r(` + py.Digit + `+))?`)

var sessionUnsafeRE = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func gate(input []byte) bool {
	return bytes.Contains(input, []byte("github.com"))
}

var (
	// errSkip はその PR だけを飛ばす失敗（移植元の KeyError・TypeError）である。
	errSkip = errors.New("skip this pull request")
	// errAbort は全体を無出力にする失敗（移植元で捕まえていない例外）である。
	errAbort = errors.New("the Python implementation raises on this input")
)

type target struct {
	ownerRepo, number string
	comments          []string
}

// input は hook の入力から読んだプロンプト・cwd・session_id（型を確かめる前の値）である。
type input struct {
	prompt, cwd  string
	sessionValue any
}

// readInput は入力を読む。nil を返したら無出力で終える。
func readInput(c *hookrt.Context) (*input, error) {
	read := &input{}
	if c.FromArgs {
		read.prompt = string(c.Input)
	} else {
		// 移植元は stdin を UTF-8 として厳密に読むので、不正なバイト列は例外で無出力になっていた。
		if !utf8.Valid(c.Input) {
			return nil, nil
		}
		value, ok := decodeJSON(string(c.Input))
		payload, isObject := value.(map[string]any)
		if !ok || !isObject {
			return nil, nil
		}
		if value := payload["prompt"]; toolresponse.Truthy(value) {
			text, ok := value.(string)
			if !ok {
				return nil, errAbort
			}
			read.prompt = text
		}
		read.cwd, _ = payload["cwd"].(string)
		read.sessionValue = payload["session_id"]
	}
	if info, err := os.Stat(read.cwd); read.cwd == "" || err != nil || !info.IsDir() {
		cwd, err := py.Getcwd()
		if err != nil {
			return nil, errAbort
		}
		read.cwd = cwd
	}
	return read, nil
}

func run(c *hookrt.Context) error {
	read, err := readInput(c)
	if err != nil || read == nil {
		return err
	}
	prompt, cwd := read.prompt, read.cwd
	if !strings.Contains(prompt, "github.com") || !ghAvailable() {
		return nil
	}
	targets := parseTargets(prompt)
	if len(targets) == 0 {
		return nil
	}
	cache := newStore()
	dirs := &dirResolver{entries: map[[2]string]*dirEntry{}}
	pulls, comments, err := fetchAll(targets, cwd, cache, dirs)
	if err != nil {
		return err
	}
	sessionID := ""
	if toolresponse.Truthy(read.sessionValue) {
		text, ok := read.sessionValue.(string)
		if !ok {
			return errAbort
		}
		sessionID = text
	}
	injected := cache.loadInjected(sessionID)
	var blocks []string
	updated, hasMerged := false, false
	for index, target := range targets {
		pull := pulls[index]
		if !toolresponse.Truthy(pull) {
			continue
		}
		_, localRepo := dirs.resolve(target.ownerRepo, cwd)
		text, err := formatPR(c.Language(), target.ownerRepo, pull, localRepo)
		if errors.Is(err, errSkip) {
			continue
		}
		if err != nil {
			return err
		}
		for _, comment := range comments[index] {
			if !toolresponse.Truthy(comment) {
				continue
			}
			line, err := formatComment(comment)
			if err != nil {
				return err
			}
			text += "\n" + line
		}
		key := target.ownerRepo + "#" + target.number
		sig := signature(target.ownerRepo, text)
		// 同じセッションで注入済みで、内容も変わっていなければ出さない。
		// マージ・コンフリクトの発生・head の更新・object の有無の変化は再注入する。
		if previous, ok := injected[key].(string); ok && previous == sig {
			continue
		}
		injected[key] = sig
		updated = true
		blocks = append(blocks, text)
		// 注記は実際に出すブロックだけで決める（抑止した PR につられて出さない）。
		hasMerged = hasMerged || pull.(map[string]any)["state"] == "MERGED"
	}
	if updated {
		cache.saveInjected(sessionID, injected)
	}
	if len(blocks) == 0 {
		return nil
	}
	language := c.Language()
	notes := messages.T(language, idNote)
	if hasMerged {
		notes += "\n" + messages.T(language, idNoteMerged)
	}
	c.Print(messages.T(language, idHeader) + "\n" + notes + "\n" + strings.Join(blocks, "\n") + "\n" + footer + "\n")
	return nil
}

// ghAvailable は PATH に gh があるかを返す（移植元の shutil.which("gh")）。無ければ静かに諦める。
func ghAvailable() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}

// decodeJSON は text 全体を 1 つの JSON の値として読む。数値は json.Number のまま持つ。
func decodeJSON(text string) (any, bool) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, false
	}
	// 値の後ろに空白以外が残っていれば、Python の json.loads と同じく壊れているとみなす。
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return value, true
}

// parseTargets は PR ごとに (owner/repo, 番号, アンカーのコメント ID) を出現順で返す。
func parseTargets(prompt string) []target {
	var targets []target
	index := map[[2]string]int{}
	for _, match := range urlRE.FindAllStringSubmatch(prompt, -1) {
		key := [2]string{match[1], match[2]}
		position, ok := index[key]
		if !ok {
			if len(targets) >= maxPRs {
				continue
			}
			position = len(targets)
			index[key] = position
			targets = append(targets, target{ownerRepo: match[1], number: match[2]})
		}
		comment := match[3]
		current := &targets[position]
		if comment != "" && !containsString(current.comments, comment) && len(current.comments) < maxComments {
			current.comments = append(current.comments, comment)
		}
	}
	return targets
}

func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

// fetchAll は PR とアンカーのコメントをまとめて並列に取る。
// 全部を 1 巡で並列に投げる。分けると、全部が時間切れになったときに hook の timeout を超えうる。
func fetchAll(targets []target, cwd string, cache *store, dirs *dirResolver) ([]any, [][]any, error) {
	pulls := make([]any, len(targets))
	comments := make([][]any, len(targets))
	errs := make([][]error, len(targets))
	var group sync.WaitGroup
	for index, target := range targets {
		comments[index] = make([]any, len(target.comments))
		errs[index] = make([]error, 1+len(target.comments))
		group.Go(abortOnPanic(&errs[index][0], func() {
			pulls[index] = fetchPR(target.ownerRepo, target.number, cwd, cache, dirs)
		}))
		for position, id := range target.comments {
			group.Go(abortOnPanic(&errs[index][1+position], func() {
				comments[index][position], errs[index][1+position] = fetchComment(target.ownerRepo, id, cwd, cache, dirs)
			}))
		}
	}
	group.Wait()
	// 移植元は結果を出現順に受け取り、最初の例外で全体を止める。
	for _, list := range errs {
		for _, err := range list {
			if err != nil {
				return nil, nil, err
			}
		}
	}
	return pulls, comments, nil
}

// abortOnPanic は、goroutine の中の panic を slot の errAbort に変えて返す。
// hookrt.Run の recover は別の goroutine の panic を拾えず、そのままだと終了コード 2 でプロンプトを止めるため。
func abortOnPanic(slot *error, body func()) func() {
	return func() {
		defer func() {
			if recover() != nil {
				*slot = errAbort
			}
		}()
		body()
	}
}

func fetchPR(ownerRepo, number, cwd string, cache *store, dirs *dirResolver) any {
	name := strings.ReplaceAll(ownerRepo, "/", "_") + "_" + number + ".json"
	if cached := cache.read(name); toolresponse.Truthy(cached) {
		return cached
	}
	dir, _ := dirs.resolve(ownerRepo, cwd)
	data := ghJSON(dir, "pr", "view", number, "--repo", ownerRepo, "--json", ghFields)
	if toolresponse.Truthy(data) {
		cache.write(name, data)
	}
	return data
}

// fetchComment は #discussion_rNNN の指す先を「どこの誰の指摘か」だけ解決する。本文は入れない。
// 数字のままだとコメントを探し回る動きを誘発するので、位置だけは確定させておく。
func fetchComment(ownerRepo, id, cwd string, cache *store, dirs *dirResolver) (any, error) {
	name := strings.ReplaceAll(ownerRepo, "/", "_") + "_c" + id + ".json"
	if cached := cache.read(name); toolresponse.Truthy(cached) {
		return cached, nil
	}
	dir, _ := dirs.resolve(ownerRepo, cwd)
	data := ghJSON(dir, "api", "repos/"+ownerRepo+"/pulls/comments/"+id)
	if !toolresponse.Truthy(data) {
		return nil, nil
	}
	fields, ok := data.(map[string]any)
	if !ok {
		return nil, errAbort
	}
	var user any = "?"
	if value := fields["user"]; toolresponse.Truthy(value) {
		owner, ok := value.(map[string]any)
		if !ok {
			return nil, errAbort
		}
		if login, ok := owner["login"]; ok {
			user = login
		}
	}
	entry := map[string]any{
		"id": id, "user": user, "path": orValue(fields["path"], "?"),
		"line": orValue(fields["line"], orValue(fields["original_line"], "?")),
	}
	cache.write(name, entry)
	return entry, nil
}

// orValue は Python の value or fallback である。
func orValue(value, fallback any) any {
	if toolresponse.Truthy(value) {
		return value
	}
	return fallback
}

// ghJSON は gh を dir で実行し、出力を JSON として読む。失敗・時間切れ・空・壊れた JSON なら nil を返す。
func ghJSON(dir string, args ...string) any {
	output, ok := hookexec.Output(dir, ghTimeout, "gh", args...)
	if !ok || py.Strip(output) == "" {
		return nil
	}
	value, ok := decodeJSON(output)
	if !ok {
		return nil
	}
	return value
}

// gitOut は `git -C dir args...` の stdout の両端の空白を除いて返す。失敗したら空を返す。
func gitOut(dir string, args ...string) string {
	output, ok := hookexec.Output("", gitTimeout, "git", append([]string{"-C", dir}, args...)...)
	if !ok {
		return ""
	}
	return py.Strip(output)
}

func gitOK(dir string, args ...string) bool {
	_, ok := hookexec.Output("", gitTimeout, "git", append([]string{"-C", dir}, args...)...)
	return ok
}

// --- ローカルの clone の特定 ---------------------------------------------------

// dirResolver は 1 プロンプトの中での (gh の実行先, その PR のローカルの clone) を覚える。
// 1 回のプロンプトで PR ごとに何度も引くので、git の呼び出しを繰り返さない。
type dirResolver struct {
	mutex   sync.Mutex
	entries map[[2]string]*dirEntry
}

type dirEntry struct {
	once      sync.Once
	ghDir     string
	localRepo string
}

// resolve は gh を実行するディレクトリと、PR のローカルの clone（無ければ空）を返す。
// clone を取り違えると「main にいて head は未 checkout」といった嘘を注入するので、origin が一致したときだけ後者を返す。
// gh 自体は認証の都合でどこかのリポジトリの中から実行する必要があるため、前者は必ず埋める。
func (r *dirResolver) resolve(ownerRepo, cwd string) (string, string) {
	r.mutex.Lock()
	entry, ok := r.entries[[2]string{ownerRepo, cwd}]
	if !ok {
		entry = &dirEntry{}
		r.entries[[2]string{ownerRepo, cwd}] = entry
	}
	r.mutex.Unlock()
	entry.once.Do(func() { entry.ghDir, entry.localRepo = resolveDir(ownerRepo, cwd) })
	return entry.ghDir, entry.localRepo
}

func resolveDir(ownerRepo, cwd string) (string, string) {
	if originMatches(cwd, ownerRepo) {
		return cwd, cwd
	}
	repositories := workspaceRepositories(cwd)
	for _, repository := range repositories {
		if originMatches(repository, ownerRepo) {
			return repository, repository
		}
	}
	if len(repositories) > 0 {
		return repositories[0], ""
	}
	return cwd, ""
}

// originMatches は dir の clone の origin が ownerRepo の GitHub のリポジトリかを返す。
// 同名の owner/repo を別のホストに持つ clone を掴まないよう、github.com を含むことも見る。
func originMatches(dir, ownerRepo string) bool {
	origin := py.Lower(gitOut(dir, "remote", "get-url", "origin"))
	if !strings.Contains(origin, "github.com") {
		return false
	}
	origin = strings.TrimSuffix(origin, ".git")
	target := py.Lower(ownerRepo)
	return strings.HasSuffix(origin, ":"+target) || strings.HasSuffix(origin, "/"+target)
}

func repositoryRoot(dir string) (string, bool) {
	root := gitOut(dir, "rev-parse", "--show-toplevel")
	if root == "" {
		return "", false
	}
	resolved, err := py.Realpath(root)
	return resolved, err == nil
}

// workspaceRepositories は、cwd が Git 管理外で直下に Git のリポジトリが 2 つ以上あるときだけ、それらをバイト列の順に返す。
func workspaceRepositories(cwd string) []string {
	cwd, err := py.Realpath(cwd)
	if err != nil {
		return nil
	}
	if _, ok := repositoryRoot(cwd); ok {
		return nil
	}
	entries, err := os.ReadDir(cwd)
	if err != nil {
		return nil
	}
	var repositories []string
	for _, entry := range entries {
		// symlink は辿らない（移植元の is_dir(follow_symlinks=False)）。
		if !entry.IsDir() {
			continue
		}
		path := py.Join(cwd, entry.Name())
		marker, err := os.Stat(py.Join(path, ".git"))
		if err != nil || (!marker.IsDir() && !marker.Mode().IsRegular()) {
			continue
		}
		candidate, err := py.Realpath(path)
		if err != nil {
			continue
		}
		if root, ok := repositoryRoot(candidate); ok && root == candidate {
			repositories = append(repositories, candidate)
		}
	}
	if len(repositories) < 2 {
		return nil
	}
	sort.Strings(repositories)
	return repositories
}

// localState は PR の head の object がローカルで使えるかだけを報告する（状態の行のカタログの ID を返す）。既存の worktree の利用先は案内しない。
func localState(localDir string, headOID any) string {
	oid, isString := headOID.(string)
	if current := gitOut(localDir, "rev-parse", "HEAD"); current != "" && isString && oid != "" && current == oid {
		return idLocalHeadMatches
	}
	if toolresponse.Truthy(headOID) && gitOK(localDir, "cat-file", "-e", toolresponse.PyStr(headOID)+"^{commit}") {
		return idLocalAvailable
	}
	return idLocalMissing
}

// --- 整形 ----------------------------------------------------------------------

// sanitize は他人が決められる文字列（タイトル・ブランチ名・ファイルのパス）の、改行での構造崩しと閉じタグの偽装だけを潰す。
// 文字列でない真の値は、移植元では例外になり全体が無出力になる。
func sanitize(value any) (string, error) {
	if !toolresponse.Truthy(value) {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", errAbort
	}
	return strings.ReplaceAll(strings.Join(py.Fields(text), " "), "</", "<\u200b/"), nil
}

// prefix は Python の text[:n]（文字単位）である。
func prefix(text string, n int) string {
	runes := []rune(text)
	return string(runes[:min(n, len(runes))])
}

// shorten はパスを ~ で縮める。前方一致ではなく区切りまで見る（/Users/alice に対して /Users/alice-other を ~-other にしない）。
func shorten(path string) string {
	home, err := py.Realpath(py.Expanduser("~"))
	if err != nil {
		return path
	}
	resolved, err := py.Realpath(path)
	if err != nil {
		return path
	}
	if resolved == home {
		return "~"
	}
	if strings.HasPrefix(resolved, home+"/") {
		return "~" + resolved[len(home):]
	}
	return resolved
}

// formatPR は PR 1 件分の 3 行を組み立てる。移植元の評価の順に項目を読み、欠けた項目（KeyError）と
// 型の違い（TypeError）は errSkip、捕まえていない例外は errAbort を返す。
func formatPR(language i18n.Language, ownerRepo string, value any, localRepo string) (string, error) {
	pull, ok := value.(map[string]any)
	if !ok {
		return "", errSkip
	}
	field := func(key string) (any, error) {
		value, ok := pull[key]
		if !ok {
			return nil, errSkip
		}
		return value, nil
	}
	label, err := stateLabel(pull, field)
	if err != nil {
		return "", err
	}
	var mergedAt any
	if commit := pull["mergeCommit"]; toolresponse.Truthy(commit) {
		fields, ok := commit.(map[string]any)
		if !ok {
			return "", errAbort
		}
		mergedAt = fields["oid"]
	}
	// 対応するローカルの clone を確認できないリポジトリでは、パスもローカルの状態も書かない。
	// 取り違えたパスを出すと、無関係なローカルのファイルを調査してしまう。
	where, stateLine := "", idLocalNoClone
	if localRepo != "" {
		if _, err := field("headRefName"); err != nil {
			return "", err
		}
		headOID, err := field("headRefOid")
		if err != nil {
			return "", err
		}
		where = " repo=" + shorten(localRepo)
		stateLine = localState(localRepo, headOID)
	}
	var values []string
	for _, key := range []string{"number", "title", "headRefName", "headRefOid", "baseRefName"} {
		value, err := field(key)
		if err != nil {
			return "", err
		}
		switch key {
		case "number":
			values = append(values, toolresponse.PyStr(value))
		case "headRefOid":
			text, ok := value.(string)
			if !ok {
				return "", errSkip
			}
			values = append(values, prefix(text, 8))
		default:
			text, err := sanitize(value)
			if err != nil {
				return "", err
			}
			values = append(values, text)
		}
	}
	merged := ""
	if toolresponse.Truthy(mergedAt) {
		// 文字列でない oid は、移植元では ' merge=' との連結か切り出しで TypeError になる。
		text, ok := mergedAt.(string)
		if !ok {
			return "", errSkip
		}
		merged = " merge=" + prefix(text, 8)
	}
	var counts []string
	for _, key := range []string{"additions", "deletions", "changedFiles"} {
		value, err := field(key)
		if err != nil {
			return "", err
		}
		counts = append(counts, toolresponse.PyStr(value))
	}
	return ownerRepo + "#" + values[0] + " " + label + " \"" + values[1] + "\"\n" +
		"  head=" + values[2] + "@" + values[3] + " base=" + values[4] + merged +
		" +" + counts[0] + "-" + counts[1] + " " + counts[2] + "f" + where + "\n" +
		messages.T(language, stateLine), nil
}

// stateLabel は PR の状態の表記（OPEN・DRAFT・CONFLICT・MERGED(into ...) など）を組み立てる。
func stateLabel(pull map[string]any, field func(string) (any, error)) (string, error) {
	isDraft, err := field("isDraft")
	if err != nil {
		return "", err
	}
	state, err := field("state")
	if err != nil {
		return "", err
	}
	// DRAFT は state=OPEN が自明なので 1 語にまとめる。
	label := toolresponse.PyStr(state)
	if toolresponse.Truthy(isDraft) {
		label = "DRAFT"
	}
	// コンフリクトは open な PR でしか意味を持たない。閉じた PR に付けると不要な作業を誘発する。
	if state == "OPEN" && pull["mergeable"] == "CONFLICTING" {
		label += " CONFLICT"
	}
	// stacked PR は main 以外にマージされる。"MERGED" だけだと main にあると読まれるので明示する。
	if state == "MERGED" {
		base, err := field("baseRefName")
		if err != nil {
			return "", err
		}
		if base != "main" && base != "master" {
			text, err := sanitize(base)
			if err != nil {
				return "", err
			}
			label += "(into " + text + ", NOT main)"
		}
	}
	return label, nil
}

// formatComment はアンカーのコメント 1 件分の行を組み立てる。項目が欠けていれば、移植元と同じく全体を無出力にする。
func formatComment(value any) (string, error) {
	comment, ok := value.(map[string]any)
	if !ok {
		return "", errAbort
	}
	var parts []any
	for _, key := range []string{"id", "user", "path", "line"} {
		part, ok := comment[key]
		if !ok {
			return "", errAbort
		}
		parts = append(parts, part)
	}
	path, err := sanitize(parts[2])
	if err != nil {
		return "", err
	}
	return "  anchored comment r" + toolresponse.PyStr(parts[0]) + " by @" + toolresponse.PyStr(parts[1]) +
		" at " + path + ":" + toolresponse.PyStr(parts[3]), nil
}

// signature は再注入するかの判定の鍵である。時間で動く表記は出力に含めていないので、表示の文面から作っても安定する。
func signature(ownerRepo, text string) string {
	sum := sha256.Sum256([]byte(ownerRepo + "|" + text))
	return hex.EncodeToString(sum[:])[:16]
}

// --- キャッシュと注入済みの記録 ------------------------------------------------

// store は取得のキャッシュと注入済みの記録の置き場である。目的も寿命も違う。
//
//	取得のキャッシュ … 鍵は repo#num           / gh の呼び出しを減らす       / 全セッションで共有 / 90 秒
//	注入済みの記録   … 鍵は session + repo#num / 同じ内容の再注入を止める / セッションの中     / 24 時間
//
// 置き場所が決められない（HOME が無いなど）ときは、どちらも使わずに毎回取得して注入する。
type store struct {
	dir string
}

func newStore() *store {
	dir, err := hookcache.Dir(Name)
	if err != nil {
		return &store{}
	}
	return &store{dir: dir}
}

func (s *store) sessionDir() string {
	return filepath.Join(s.dir, "sessions")
}

func (s *store) read(name string) any {
	if s.dir == "" {
		return nil
	}
	path := filepath.Join(s.dir, name)
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) >= cacheTTL {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	value, ok := decodeJSON(string(data))
	if !ok {
		return nil
	}
	return value
}

func (s *store) write(name string, value any) {
	if s.dir == "" {
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	_ = hookcache.WriteFile(s.dir, name, data)
}

// sessionFile は注入済みの記録のファイル名である。session_id を安全な文字に絞るので、ディレクトリの外を指さない。
func sessionFile(sessionID string) string {
	safe := sessionUnsafeRE.ReplaceAllString(sessionID, "")
	return safe[:min(len(safe), 64)] + ".json"
}

func (s *store) loadInjected(sessionID string) map[string]any {
	if sessionID == "" || s.dir == "" {
		return map[string]any{}
	}
	data, err := os.ReadFile(filepath.Join(s.sessionDir(), sessionFile(sessionID)))
	if err != nil {
		return map[string]any{}
	}
	value, _ := decodeJSON(string(data))
	if injected, ok := value.(map[string]any); ok {
		return injected
	}
	return map[string]any{}
}

func (s *store) saveInjected(sessionID string, injected map[string]any) {
	if sessionID == "" || s.dir == "" {
		return
	}
	data, err := json.Marshal(injected)
	if err != nil {
		return
	}
	if hookcache.WriteFile(s.sessionDir(), sessionFile(sessionID), data) != nil {
		return
	}
	s.pruneSessions()
}

// pruneSessions は、終わったセッションの記録が溜まり続けないよう古いものを捨てる。
// 書き込みがあったときだけ呼ぶので、走査の頻度は低い。
func (s *store) pruneSessions() {
	entries, err := os.ReadDir(s.sessionDir())
	if err != nil {
		return
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) <= sessionTTL {
			continue
		}
		_ = os.Remove(filepath.Join(s.sessionDir(), entry.Name()))
	}
}
