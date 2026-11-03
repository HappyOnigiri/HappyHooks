// Package irreversibleguard は、実行するとユーザーにも元に戻せない操作と、秘密ファイルへの直接書き込みを
// PreToolUse で拒否する hook（irreversible-guard）である。
//
// 対象:
//
//	A. 資格情報の失効・削除 (トークン revoke、GitHub Secret、Keychain、IAM キー …)
//	B. レジストリへの公開・取り下げ (npm publish / gem push / twine upload …)
//	C. リモートリソースの恒久削除 (gh repo delete、terraform destroy、DROP TABLE …)
//	D. Git オブジェクトの物理破壊 (reflog expire / gc --prune=now / stash clear)
//	E. マシン上の不可逆消去 (diskutil erase* / tmutil delete)
//	F. Git 管理外の秘密ファイルへの書き込み・破壊 (.envrc / .env* / envrc.local / ~/.ssh / *.pem)
//	G. ガード自体の削除 (hhx の実行ファイル、~/.config/hhx、Claude の settings*.json、Codex の hooks.json と config.toml)
//
// 方針:
//   - force push・リモートブランチ削除のような「履歴から復元できる」操作は対象外。
//   - 判定は deny のみ（Codex は ask を解釈しない）。承認された操作はユーザー自身が実行する運用。
//   - F は Bash だけでなく Edit / Write / apply_patch にも当てる。新規作成を含め、秘密ファイルへの直接書き込みは許可しない。
//   - G は削除・移動だけを塞ぐ。編集は通常のメンテナンスなので許可する。
//   - publish 系は --dry-run が付いていれば許可する。
//   - 変数展開・eval・別シェル経由の明示的な回避は対象外（README の既知の限界）。
//
// 規則は上から順に評価し、最初に一致したもので拒否する。
// JSON として読めない入力は fail-open にせず、生の文字列を Bash のコマンドとして判定する。
package irreversibleguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/HappyOnigiri/hhx/internal/hookrt"
	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

// Name は hook の名前である。
const Name = "irreversible-guard"

// Definition は irreversible-guard の定義を返す。
func Definition() hookrt.Definition {
	return hookrt.Definition{
		Name:           Name,
		DefaultEnabled: true,
		Registrations: []hookrt.Registration{
			{Agent: hookrt.Claude, Event: "PreToolUse", Matcher: "Bash"},
			{Agent: hookrt.Claude, Event: "PreToolUse", Matcher: "Edit|Write|MultiEdit|NotebookEdit"},
			{Agent: hookrt.Codex, Event: "PreToolUse", Matcher: "Bash"},
			{Agent: hookrt.Codex, Event: "PreToolUse", Matcher: "^(apply_patch|Edit|Write)$"},
		},
		Gate: gate,
		Run:  run,
	}
}

// gateKeywords は一次ゲートの語である。これを含まない入力は判定しない。
// G 類の保護対象に合わせ、移植元の agent-config を外して hhx を足してある。
var gateKeywords = []string{
	"gh", "git", "curl", "wget", "security", "gcloud", "aws", "npm", "yarn",
	"gem", "twine", "cargo", "psql", "mysql", "mariadb", "sqlite3",
	"terraform", "tofu", "diskutil", "tmutil", "gpg", "op ", "revoke",
	".env", "envrc", ".ssh", ".pem", ".claude", ".codex", "hhx",
}

func gate(input []byte) bool {
	raw := string(input)
	for _, keyword := range gateKeywords {
		if strings.Contains(raw, keyword) {
			return true
		}
	}
	return false
}

func run(c *hookrt.Context) error {
	if c.FromArgs {
		// デバッグ経路: 引数のコマンド文字列を直接判定する。
		evaluate(c, string(c.Input))
		return nil
	}
	payload, ok := decodeObject(c.Input)
	if !ok {
		// object として読めない入力は fail-open にせず、生の文字列のまま判定に回す。
		evaluate(c, string(c.Input))
		return nil
	}
	var toolName string
	_ = json.Unmarshal(payload["tool_name"], &toolName)
	toolInput, ok := decodeMap(payload["tool_input"])
	if !ok {
		return nil
	}
	// tool_name が無い・空・文字列でないときは Bash として扱う（移植元の `tool_name or "Bash"`）。
	switch toolName {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		path := firstTruthy(toolInput["file_path"], toolInput["notebook_path"])
		if label := secretPathLabel(path); label != "" {
			c.Deny(reason(c.Language(), target{idSecretEdit, label}, idWhySecret))
		}
	case "apply_patch":
		if label := patchedSecretLabel(payload["tool_input"]); label != "" {
			c.Deny(reason(c.Language(), target{idSecretPatch, label}, idWhySecret))
		}
	default:
		// 文字列でない command は判定しない（移植元の isinstance の検査）。
		command, _ := toolInput["command"].(string)
		evaluate(c, command)
	}
	return nil
}

// decodeObject は JSON の object を、値を解釈せずに読む。object でなければ偽を返す。
func decodeObject(raw []byte) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	err := json.Unmarshal(raw, &object)
	return object, err == nil && object != nil
}

// decodeMap は JSON の object を読む。object でなければ（null を含む）偽を返す。
func decodeMap(raw json.RawMessage) (map[string]any, bool) {
	var object map[string]any
	err := json.Unmarshal(raw, &object)
	return object, err == nil && object != nil
}

// evaluate は Bash のコマンド文字列を判定し、拒否するなら出力する。
func evaluate(c *hookrt.Context, command string) {
	if found, why := analyzeCommand(command); found.id != "" {
		c.Deny(reason(c.Language(), found, why))
	}
}

// firstTruthy は Python の `a or b or ""` と同じく、偽でない最初の値を返す。
func firstTruthy(values ...any) any {
	for _, value := range values {
		if truthy(value) {
			return value
		}
	}
	return ""
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case float64:
		return typed != 0
	case string:
		return typed != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

const (
	sp = py.Space
	// rtk ラッパー経由の呼び出しも同じ扱いにする。
	rtk = `(?:rtk` + sp + `+)?`
	// 同じコマンドの中（パイプ・コマンド区切りを越えない）。
	seg      = `[^|;&]*`
	boundary = `(?:^|[` + py.SpaceChars + `;&|(])`
	// commandStart は F・G 類のコマンド名の前の境界（行頭・空白・開き括弧）である。
	commandStart = `(?:^|[` + py.SpaceChars + `(])`
	pathPrefix   = `(?:` + py.NotSpace + `*/)?`

	ghCommand  = boundary + rtk + pathPrefix + `gh` + sp + `+`
	gitCommand = boundary + rtk + pathPrefix + `git` + sp + `+`
	httpClient = boundary + `(?:curl|wget)(?:` + sp + `|$)`

	// wordEnd は語の文字の直後の \b である。パターンの末尾にだけ置く（1 文字を消費する）。
	wordEnd = py.NotWordOrEnd
	// segWord は、空白の直後に置いた `{SEG}\b` を語の文字の前で展開したものである。
	// \b の直前の文字は、SEG が空なら直前の空白（語の文字ではない）、空でなければ SEG の最後の文字になるので、
	// 「SEG が空」か「SEG が語の文字でない文字で終わる」の 2 通りに等しい。
	segWord = `(?:` + seg + `[^|;&` + py.WordChars + `])?`
	// dashOptions は `(?:-\S+\s+)*`（ハイフンで始まる引数の並び）である。
	dashOptions = `(?:-` + py.NotSpace + `+` + sp + `+)*`
)

// rule は 1 つの規則である。pattern が正規化したコマンドのどこかに一致すれば、target の表記で拒否する。
// target と why はカタログの ID である。
type rule struct {
	pattern *regexp.Regexp
	target  string
	why     string
}

func newRule(pattern, target, why string) rule {
	return rule{pattern: regexp.MustCompile(pattern), target: target, why: why}
}

// commandRules は A・C・D・E 類の規則である。移植元の COMMAND_RULES と同じ順に並べる。
var commandRules = []rule{
	// --- A. 資格情報の失効・削除 ---
	newRule(httpClient+seg+`credentials/revoke`, idRevocationAPI, idWhyCred),
	newRule(httpClient+seg+`https?://[^`+py.SpaceChars+`|;&]*revoke`,
		idRevokeHTTP, idWhyCred),
	newRule(ghCommand+`api`+seg+`revoke`, idGHAPIRevoke, idWhyCred),
	newRule(ghCommand+`auth`+sp+`+logout`+wordEnd, idGHLogout, idWhyCred),
	newRule(ghCommand+`(?:secret|variable|ssh-key|gpg-key)`+sp+`+delete`+wordEnd,
		idGHSecret, idWhyCred),
	newRule(ghCommand+`repo`+sp+`+deploy-key`+sp+`+delete`+wordEnd, idGHDeployKey, idWhyCred),
	newRule(boundary+pathPrefix+`security`+sp+`+`+dashOptions+`delete-`+py.Word,
		idKeychain, idWhyCred),
	newRule(boundary+pathPrefix+`gcloud`+sp+segWord+`revoke`+wordEnd, idGcloudRevoke, idWhyCred),
	newRule(boundary+`npm`+sp+`+token`+sp+`+revoke`+wordEnd, idNPMToken, idWhyCred),
	newRule(boundary+`gpg2?`+sp+seg+`--delete-secret-key`, idGPGSecret, idWhyCred),
	newRule(boundary+`op`+sp+`+item`+sp+`+delete`+wordEnd, idOPItem, idWhyCred),
	newRule(`--force-delete-without-recovery`, idAWSSecret, idWhyCred),
	// --- C. リモートリソースの恒久削除 ---
	newRule(ghCommand+`(?:repo|release|gist)`+sp+`+delete`+wordEnd, idGHDelete, idWhyRemote),
	newRule(ghCommand+`api`+seg+`-X`+sp+`+(?i:DELETE)`+wordEnd, idGHAPIDelete, idWhyRemote),
	newRule(boundary+pathPrefix+`gcloud`+sp+segWord+`delete`+wordEnd, idGcloudDelete, idWhyRemote),
	newRule(boundary+pathPrefix+`aws`+sp+segWord+`delete-`+py.Word, idAWSDelete, idWhyRemote),
	newRule(boundary+pathPrefix+`aws`+sp+`+s3`+sp+`+(?:rb|rm)`+wordEnd, idAWSS3, idWhyRemote),
	newRule(boundary+pathPrefix+`(?:terraform|tofu)`+sp+`+`+dashOptions+`destroy`+wordEnd, idTFDestroy, idWhyRemote),
	newRule(boundary+pathPrefix+`(?:terraform|tofu)`+sp+`+`+dashOptions+`apply`+sp+seg+`-destroy`+wordEnd,
		idTFApplyDestroy, idWhyRemote),
	newRule(boundary+pathPrefix+`mysqladmin`+sp+segWord+`drop`+wordEnd, idMysqladmin, idWhyDB),
	// --- D. Git オブジェクトの物理破壊 ---
	newRule(gitCommand+segWord+`reflog`+sp+`+expire`+wordEnd, idReflog, idWhyGit),
	// `\bgc\b{SEG}--prune`: gc の直後の \b は、SEG が空なら次の - で、空でなければ SEG の先頭の文字で成り立つ。
	newRule(gitCommand+segWord+`gc(?:[^|;&`+py.WordChars+`]`+seg+`)?--prune=(?:now|all)`+wordEnd,
		idGC, idWhyGit),
	newRule(gitCommand+segWord+`stash`+sp+`+clear`+wordEnd, idStash, idWhyGit),
	// --- E. マシン上の不可逆消去 ---
	newRule(boundary+pathPrefix+`diskutil`+sp+segWord+`(?:erase`+py.Word+`*|partitionDisk|zeroDisk|reformat)`+wordEnd,
		idDiskutil, idWhyMachine),
	newRule(boundary+pathPrefix+`diskutil`+sp+`+apfs`+sp+`+delete`+py.Word+`*`, idAPFS, idWhyMachine),
	newRule(boundary+pathPrefix+`tmutil`+sp+`+delete`+wordEnd, idTmutil, idWhyMachine),
}

// publishRules は B 類（--dry-run が付いていれば許可する公開系）の規則である。
var publishRules = []rule{
	newRule(boundary+pathPrefix+`(?:npm|pnpm)`+sp+`+(?:publish|unpublish|deprecate)`+wordEnd,
		idNPMPublish, idWhyPublish),
	newRule(boundary+pathPrefix+`yarn`+sp+`+(?:npm`+sp+`+)?publish`+wordEnd, idYarnPublish, idWhyPublish),
	newRule(boundary+pathPrefix+`gem`+sp+`+(?:push|yank)`+wordEnd, idGemPublish, idWhyPublish),
	newRule(boundary+`twine`+sp+`+upload`+wordEnd, idTwinePublish, idWhyPublish),
	newRule(boundary+pathPrefix+`cargo`+sp+`+(?:publish|yank)`+wordEnd, idCargoPublish, idWhyPublish),
}

var (
	// 二次ゲート: 語の境界つきで判定し直す（"legit" の git を拾わない）。境界は移植元と同じく ASCII で見る。
	secondGateWords = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])(?:git|gh|curl|wget|security|gcloud|aws|npm|pnpm|` +
		`yarn|gem|twine|cargo|psql|mysql|mariadb|mysqladmin|sqlite3|terraform|tofu|diskutil|tmutil|` +
		`rm|mv|unlink|shred|srm|trash|truncate|sed|perl|tee|gpg|gpg2|op)(?:[^A-Za-z0-9_]|$)`)
	secondGatePaths = regexp.MustCompile(`\.env|envrc|\.ssh|\.pem|\.claude/|\.codex/`)

	methodFlagRE = regexp.MustCompile(`--method[=` + py.SpaceChars + `]+`)
	joinedXFlag  = regexp.MustCompile(`(^|` + sp + `)-X([A-Za-z]+)`)

	deleteMethodRE = regexp.MustCompile(`-X` + sp + `+(?i:DELETE)` + wordEnd)
	cloudHostRE    = regexp.MustCompile(`api\.github\.com|googleapis\.com|amazonaws\.com`)
	httpClientRE   = regexp.MustCompile(httpClient)
	dbClientRE     = regexp.MustCompile(boundary + pathPrefix + `(?:psql|mysql|mariadb|sqlite3)(?:` + sp + `|$)`)
	// 先頭の \b は語の文字 D・T の直前にあるので、1 文字を消費する NotWordOrStart で書ける（パターンの先頭）。
	destructiveSQL = regexp.MustCompile(`(?i)` + py.NotWordOrStart +
		`(?:DROP` + sp + `+(?:TABLE|DATABASE|SCHEMA)|TRUNCATE` + sp + `)`)
)

// normalize は表記ゆれをそろえる。--method=X / --method X / -XDELETE をすべて "-X X" に寄せ、改行を空白にする。
func normalize(command string) string {
	normalized := strings.ReplaceAll(command, "\n", " ")
	normalized = methodFlagRE.ReplaceAllString(normalized, "-X ")
	return joinedXFlag.ReplaceAllString(normalized, "${1}-X ${2}")
}

// analyzeCommand は Bash のコマンド文字列を判定し、拒否するなら対象の表記と理由を、通すなら空文字列を返す。
func analyzeCommand(command string) (found target, why string) {
	if command == "" {
		return target{}, ""
	}
	if !secondGateWords.MatchString(command) && !secondGatePaths.MatchString(command) {
		return target{}, ""
	}
	command = stripWrittenHeredocs(command)
	normalized := normalize(command)
	for _, rule := range commandRules {
		if rule.pattern.MatchString(normalized) {
			return target{id: rule.target}, rule.why
		}
	}
	if !strings.Contains(normalized, "--dry-run") {
		for _, rule := range publishRules {
			if rule.pattern.MatchString(normalized) {
				return target{id: rule.target}, rule.why
			}
		}
	}
	// HTTP クライアントによるクラウド API の DELETE（URL と -X の順序は問わない）。
	if httpClientRE.MatchString(normalized) && deleteMethodRE.MatchString(normalized) &&
		cloudHostRE.MatchString(normalized) {
		return target{id: idHTTPDelete}, idWhyRemote
	}
	// DB クライアント経由の破壊 SQL（SQL ファイル内の文字列だけでは発火しない）。
	if dbClientRE.MatchString(normalized) && destructiveSQL.MatchString(normalized) {
		return target{id: idDB}, idWhyDB
	}
	if found := checkSecretFiles(normalized); found.id != "" {
		return found, idWhySecret
	}
	if found := checkGuardFiles(normalized); found.id != "" {
		return found, idWhyGuard
	}
	return target{}, ""
}

// segments は `re.split(r"[|;&]", ...)` と同じく、パイプとコマンド区切りで分ける。
func segments(command string) []string {
	return strings.FieldsFunc(command, func(r rune) bool { return r == '|' || r == ';' || r == '&' })
}

var (
	deleteVerbRE  = regexp.MustCompile(commandStart + pathPrefix + `(?:rm|unlink|shred|srm|truncate)` + sp)
	inPlaceToolRE = regexp.MustCompile(commandStart + `(?:sed|perl)` + sp)
	inPlaceFlagRE = regexp.MustCompile(sp + `-i`)
)

// checkSecretFiles は秘密ファイルへの破壊的な操作（削除・移動・in-place 書き換え・上書きリダイレクト・tee）を探し、
// 見つかれば対象の表記を返す。
func checkSecretFiles(normalized string) target {
	for _, segment := range segments(normalized) {
		scanner := newSecretScanner(segment)
		tokens := scanner.tokens()
		if len(tokens) == 0 {
			continue
		}
		shown := tokens[0]
		switch {
		case deleteVerbRE.MatchString(segment):
			return target{idSecretRM, shown}
		case inPlaceToolRE.MatchString(segment) && inPlaceFlagRE.MatchString(segment):
			return target{idSecretInPlace, shown}
		case scanner.movesSecret():
			return target{idSecretMV, shown}
		case scanner.redirectsToSecret():
			return target{idSecretRedirect, shown}
		case scanner.teesToSecret():
			return target{idSecretTee, shown}
		}
	}
	return target{}
}

// secretPathLabel は Edit / Write / apply_patch の対象のパスが秘密ファイルなら表示名を、違えば空文字列を返す。
func secretPathLabel(value any) string {
	path, _ := value.(string)
	if path == "" {
		return ""
	}
	trimmed := strings.TrimRight(path, "/")
	base := trimmed[strings.LastIndex(trimmed, "/")+1:]
	switch {
	case base == ".envrc" || base == "envrc.local":
		return base
	case base == ".env" || (strings.HasPrefix(base, ".env.") && !envTemplates[base[len(".env."):]]):
		return base
	case strings.HasSuffix(base, ".pem"):
		return base
	}
	parts := strings.Split(strings.ReplaceAll(path, `\`, "/"), "/")
	for _, part := range parts[:len(parts)-1] {
		if part != ".ssh" {
			continue
		}
		if strings.HasSuffix(base, ".pub") || strings.HasPrefix(base, "known_hosts") || base == "config" {
			return ""
		}
		return ".ssh/" + base
	}
	return ""
}

// envTemplates は秘密ファイルとみなさない .env.<名前> の名前（_ENV_OK）である。
var envTemplates = map[string]bool{"example": true, "sample": true, "template": true, "dist": true}

// patchHeaderRE は apply_patch の対象のファイルの行である。移植元は tool_input を JSON 文字列にしてから当てていたので、
// パスは JSON でエスケープされる文字（\ と " と制御文字）の手前までになる。
var patchHeaderRE = regexp.MustCompile(`\*\*\* (?:Add|Update|Delete) File: ([^\\"\x00-\x1f]+)`)

// patchedSecretLabel は apply_patch の tool_input の中の文字列を、移植元の json.dumps と同じ順に辿り、
// 最初に見つかった秘密ファイルの表示名を返す。
func patchedSecretLabel(raw json.RawMessage) string {
	for _, text := range jsonStrings(raw) {
		for _, match := range patchHeaderRE.FindAllStringSubmatch(text, -1) {
			if label := secretPathLabel(py.Strip(match[1])); label != "" {
				return label
			}
		}
	}
	return ""
}

// jsonStrings は raw の JSON に含まれる文字列（object のキーを含む）を、Python の json.loads から json.dumps に
// 戻したときと同じ順に返す。object の重複したキーは、最初に現れた位置に最後の値を置く（Python の dict と同じ）。
func jsonStrings(raw json.RawMessage) []string {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeOrdered(decoder)
	if err != nil {
		return nil
	}
	var out []string
	collectStrings(value, &out)
	return out
}

// orderedObject は、キーの順序を保った JSON の object である。
type orderedObject struct {
	keys   []string
	values map[string]any
}

func decodeOrdered(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		object := &orderedObject{values: map[string]any{}}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, _ := keyToken.(string)
			value, err := decodeOrdered(decoder)
			if err != nil {
				return nil, err
			}
			if _, seen := object.values[key]; !seen {
				object.keys = append(object.keys, key)
			}
			object.values[key] = value
		}
		_, err = decoder.Token()
		return object, err
	case '[':
		var items []any
		for decoder.More() {
			item, err := decodeOrdered(decoder)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		_, err = decoder.Token()
		return items, err
	default:
		return nil, errors.New("unexpected delimiter")
	}
}

func collectStrings(value any, out *[]string) {
	switch typed := value.(type) {
	case string:
		*out = append(*out, typed)
	case []any:
		for _, item := range typed {
			collectStrings(item, out)
		}
	case *orderedObject:
		for _, key := range typed.keys {
			*out = append(*out, key)
			collectStrings(typed.values[key], out)
		}
	}
}
