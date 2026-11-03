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
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

// --- G. ガードファイル ---------------------------------------------------------

// 守るのは hhx の実行ファイル・hhx の設定ディレクトリ・両 CLI の hook の登録ファイルである。
// 移植元が守っていた旧配布先（~/.claude/hooks・~/.codex/hooks）と dotfiles の正本は、hook の実体がもう無いので外した。
const (
	// registrationCore は Claude の settings*.json と Codex の hooks.json・config.toml である（移植元の正規表現を引き継ぐ）。
	registrationCore = `\.claude/settings[` + py.WordChars + `.]*\.json|\.codex/(?:hooks\.json|config\.toml)`
	// guardPrefix は _PRE（トークンの直前に許す文字）である。行頭の ^ は、checkGuardFiles がセグメントの前に
	// 空白を 1 つ足すことで表す（走査を途中から再開したときに ^ が一致しないようにするため）。
	guardPrefix = `[` + py.SpaceChars + `'"=(/>]`
	// hhxPrefix は hhx の保護対象のトークンの直前に許す文字である。/ を含めないのは、/tmp/x/~/.config/hhx のような
	// 別のパスの途中から一致させないためである。
	hhxPrefix = `[` + py.SpaceChars + `'"=(>]`
	// hhxTerminator は hhx の保護対象の直後の 1 文字である。hhx.bak のような別名のファイルまで止めないために置く。
	hhxTerminator = `(?:[^` + py.WordChars + `.\-]|$)`
	// pathTail は保護対象のディレクトリの配下（移植元の hooks(?:/[^\s'"|;&]*)? と同じ形）である。
	pathTail = `(?:/[^` + py.SpaceChars + `'"|;&]*)?`
)

// executable は hhx の実行ファイルのパスを返す。テストで差し替える。
var executable = os.Executable

var (
	moveVerbRE = regexp.MustCompile(commandStart + pathPrefix + `(?:rm|unlink|shred|srm|mv|trash)` + sp)
	guardSkip  = []string{"__pycache__", ".ruff_cache", ".pytest_cache", ".DS_Store"}
)

// hhxCores は hhx の保護対象を、トークン全体に一致させる正規表現の断片で返す。
// 絶対パスに加え、ホームディレクトリの下にあるものは ~/…・$HOME/…・${HOME}/… の表記でも一致させる。
// 末尾だけで照合しないのは、/tmp/project/.config/hhx のような別の場所にある同名のパスまで止めないためである。
func hhxCores() []string {
	// HOME が無くてもシェルは ~ をパスワードデータベースのホームへ展開するので、同じ順で探す。
	// どちらからも得られないとき（"~"）は、ホームの下の保護対象を足さない。
	// ホームが / のときは home を空にし、home+"/" が / になるようにする。
	home := py.Home()
	homeKnown := home != "~"
	home = strings.TrimRight(home, "/")
	var cores []string
	seen := map[string]bool{}
	appendCore := func(core string) {
		if !seen[core] {
			seen[core] = true
			cores = append(cores, core)
		}
	}
	add := func(path, tail string) {
		if strings.Trim(path, "/") == "" {
			return
		}
		appendCore(`/+` + regexp.QuoteMeta(strings.TrimLeft(path, "/")) + tail)
		if homeKnown && strings.HasPrefix(path, home+"/") {
			relative := regexp.QuoteMeta(path[len(home)+1:]) + tail
			appendCore(`(?:~|\$HOME|\$\{HOME\})/` + relative)
		}
	}
	if path, err := executable(); err == nil && strings.HasPrefix(path, "/") {
		add(path, "")
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			add(resolved, "")
		}
	}
	if homeKnown {
		add(home+"/.config/hhx", pathTail)
	}
	return cores
}

// guardPattern は、登録ファイルに一致する `_PRE((?:\S*/)?GUARD_CORE)` と、hhx の保護対象に一致する
// `hhxPrefix(HHX_CORE)` + 終端の 1 文字、を並べて組み立てる。
// 1 番目の括弧が登録ファイルのトークン、2 番目の括弧が hhx の保護対象のトークン（直後の 1 文字を含めない）である。
func guardPattern() *regexp.Regexp {
	pattern := guardPrefix + `((?:` + py.NotSpace + `*/)?(?:` + registrationCore + `))`
	if cores := hhxCores(); len(cores) > 0 {
		pattern += `|` + hhxPrefix + `((?:` + strings.Join(cores, "|") + `))` + hhxTerminator
	}
	return regexp.MustCompile(pattern)
}

// checkGuardFiles はガード（hhx・hook の登録）の削除・移動を探し、見つかれば対象の表記を返す。
// 編集とキャッシュの掃除は対象外である。
func checkGuardFiles(normalized string) target {
	var pattern *regexp.Regexp
	for _, segment := range segments(normalized) {
		if !moveVerbRE.MatchString(segment) {
			continue
		}
		if pattern == nil {
			pattern = guardPattern()
		}
		// 移植元の re.finditer はトークンの終わりから次を探す。hhx の保護対象は終端の 1 文字まで消費するので、
		// FindAll を使わずにトークンの終わりから探し直す（読み飛ばしたトークンの直後の区切り文字を、次の直前の文字に使う）。
		padded := " " + segment
		for position := 0; position < len(padded); {
			match := pattern.FindStringSubmatchIndex(padded[position:])
			if match == nil {
				break
			}
			start, end := match[2], match[3]
			if start < 0 {
				start, end = match[4], match[5]
			}
			token := padded[position+start : position+end]
			if !containsAny(token, guardSkip) {
				return target{idGuard, token}
			}
			position += end
		}
	}
	return target{}
}

func containsAny(text string, words []string) bool {
	for _, word := range words {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

// secretScanner は 1 つのセグメントの中から、移植元の SECRET_CORE（秘密ファイルのトークン）を探す。
//
// 移植元の正規表現は否定の先読み・後読みを使っており、RE2 では書けない。先読みが失敗すると Python は
// 任意部分を短くしてやり直す（バックトラックする）ので、候補を最長で 1 つ取って確かめるだけでは同じにならない。
// ここでは同じ優先順で候補を試す手書きの照合にし、位置ごとの結果を覚えて入力の長さに比例する時間で終える。
//
//	SECRET_CORE  = (?:\S*/)?(?:_ENV|_ENVRC_LOCAL|_SSH|_PEM)
//	_ENV         = \.env(?:rc)?(?:\.(?!OK(?![\w.-]))[\w-][\w.-]*)?(?![\w.\-/])   OK = example|sample|template|dist
//	_ENVRC_LOCAL = envrc\.local(?![\w.\-/])
//	_SSH         = \.ssh(?:/(?!known_hosts|config(?![\w.-]))[\w.@-]+(?<!\.pub))?/?(?![\w.\-/])
//	_PEM         = [\w.@-]*\.pem(?![\w.\-/])
//
// 位置は rune 単位で数える（Python の str の添字と同じ）。
type secretScanner struct {
	text []rune
	// nameEnd[i] は i から始まる [\w.-] の並びの終わり、pathEnd[i] は [\w.@-] の並びの終わりである。
	nameEnd, pathEnd []int
	// bestSlash[i] は i から始まる \S の並びの中で、直後から秘密ファイルの名前が始まる最も右の / の位置（無ければ -1）である。
	bestSlash []int
	// ends は位置ごとの coreName の結果（未計算は -2、不一致は -1）である。
	ends []int
}

func newSecretScanner(segment string) *secretScanner {
	text := []rune(segment)
	size := len(text)
	scanner := &secretScanner{
		text: text, nameEnd: make([]int, size+1), pathEnd: make([]int, size+1),
		bestSlash: make([]int, size+1), ends: make([]int, size+1),
	}
	scanner.nameEnd[size], scanner.pathEnd[size], scanner.bestSlash[size] = size, size, -1
	for index := size - 1; index >= 0; index-- {
		scanner.nameEnd[index], scanner.pathEnd[index] = index, index
		if isNameChar(text[index]) {
			scanner.nameEnd[index] = scanner.nameEnd[index+1]
		}
		if isNameChar(text[index]) || text[index] == '@' {
			scanner.pathEnd[index] = scanner.pathEnd[index+1]
		}
	}
	for index := range scanner.ends {
		scanner.ends[index] = -2
	}
	for index := size - 1; index >= 0; index-- {
		switch {
		case py.IsSpace(text[index]):
			scanner.bestSlash[index] = -1
		case scanner.bestSlash[index+1] >= 0:
			scanner.bestSlash[index] = scanner.bestSlash[index+1]
		case text[index] == '/' && scanner.coreName(index+1) >= 0:
			scanner.bestSlash[index] = index
		default:
			scanner.bestSlash[index] = -1
		}
	}
	return scanner
}

// isNameChar は [\w.-] である。
func isNameChar(r rune) bool {
	return py.IsWord(r) || r == '.' || r == '-'
}

// endsName は先読み (?![\w.\-/]) が成り立つか（index が末尾か、その文字が [\w.\-/] でないか）を返す。
func (s *secretScanner) endsName(index int) bool {
	return index >= len(s.text) || (!isNameChar(s.text[index]) && s.text[index] != '/')
}

// endsNameStrict は先読み (?![\w.-]) が成り立つかを返す（/ を含まない）。
func (s *secretScanner) endsNameStrict(index int) bool {
	return index >= len(s.text) || !isNameChar(s.text[index])
}

func (s *secretScanner) hasAt(index int, literal string) bool {
	for _, r := range literal {
		if index >= len(s.text) || s.text[index] != r {
			return false
		}
		index++
	}
	return true
}

// core は SECRET_CORE を index から照合し、一致すればその終わりを、しなければ -1 を返す。
// (?:\S*/)? は貪欲なので、右の / から順に試し、どれも駄目なら接頭辞なしで試す。
func (s *secretScanner) core(index int) int {
	if slash := s.bestSlash[index]; slash >= 0 {
		return s.coreName(slash + 1)
	}
	return s.coreName(index)
}

// coreName は _ENV|_ENVRC_LOCAL|_SSH|_PEM をこの順に index から照合する。
func (s *secretScanner) coreName(index int) int {
	if s.ends[index] != -2 {
		return s.ends[index]
	}
	end := s.env(index)
	if end < 0 && s.hasAt(index, "envrc.local") && s.endsName(index+len("envrc.local")) {
		end = index + len("envrc.local")
	}
	if end < 0 {
		end = s.ssh(index)
	}
	if end < 0 {
		end = s.pem(index)
	}
	s.ends[index] = end
	return end
}

// env は _ENV である。rc の有無はこの順に試す。
// 名前の部分 [\w-][\w.-]* は、終わりの先読みがあるので [\w.-] の並びを最後まで取ったときにしか成り立たない。
// 並び全体が OK の語ならその部分は付けられず、付けないと直後の . が先読みで弾かれる。
func (s *secretScanner) env(index int) int {
	if !s.hasAt(index, ".env") {
		return -1
	}
	starts := []int{index + len(".env")}
	if s.hasAt(index+len(".env"), "rc") {
		starts = []int{index + len(".envrc"), index + len(".env")}
	}
	for _, start := range starts {
		if s.hasAt(start, ".") {
			nameStart := start + 1
			nameEnd := s.nameEnd[nameStart]
			if nameEnd > nameStart && s.text[nameStart] != '.' &&
				!envTemplates[string(s.text[nameStart:nameEnd])] && s.endsName(nameEnd) {
				return nameEnd
			}
		}
		if s.endsName(start) {
			return start
		}
	}
	return -1
}

// ssh は _SSH である。/ に続く名前 [\w.@-]+ は長い順に試す。
// 最長でない長さで終わりの先読みが成り立つのは、次の文字が @（[\w.@-] に入り、[\w.\-/] に入らない）のときだけである。
func (s *secretScanner) ssh(index int) int {
	if !s.hasAt(index, ".ssh") {
		return -1
	}
	after := index + len(".ssh")
	nameStart := after + 1
	if s.hasAt(after, "/") && !s.hasAt(nameStart, "known_hosts") &&
		(!s.hasAt(nameStart, "config") || !s.endsNameStrict(nameStart+len("config"))) {
		longest := s.pathEnd[nameStart]
		for end := longest; end > nameStart; end-- {
			if end < longest && s.text[end] != '@' {
				continue
			}
			if end >= 4 && string(s.text[end-4:end]) == ".pub" {
				continue
			}
			if result := s.optionalSlash(end); result >= 0 {
				return result
			}
		}
	}
	return s.optionalSlash(after)
}

// optionalSlash は `/?(?![\w.\-/])` を index から照合する。/ は貪欲に取る。
func (s *secretScanner) optionalSlash(index int) int {
	if s.hasAt(index, "/") && s.endsName(index+1) {
		return index + 1
	}
	if s.endsName(index) {
		return index
	}
	return -1
}

// pem は _PEM である。[\w.@-]* は貪欲なので、並びの中の .pem を右から順に試す。
func (s *secretScanner) pem(index int) int {
	for start := s.pathEnd[index] - len(".pem"); start >= index; start-- {
		if s.hasAt(start, ".pem") && s.endsName(start+len(".pem")) {
			return start + len(".pem")
		}
	}
	return -1
}

// isPrefixChar は _PRE（トークンの直前に許す文字 [\s'"=(/>]）である。
// > が要るのは空白なしのリダイレクト（`echo x >.env`）を拾うためである。
func isPrefixChar(r rune) bool {
	return py.IsSpace(r) || strings.ContainsRune(`'"=(/>`, r)
}

// tokens は `re.finditer(_PRE(SECRET_CORE))` の各一致のトークンを、移植元と同じ順に返す。
func (s *secretScanner) tokens() []string {
	var found []string
	position := 0
	for {
		start, end := s.nextToken(position)
		if start < 0 {
			return found
		}
		found = append(found, string(s.text[start:end]))
		position = end
	}
}

// nextToken は position 以降で最初に一致するトークンの範囲を返す。
// 一致の開始は _PRE の位置で、`^` は文字列の先頭でだけ、文字クラスより先に試す。
func (s *secretScanner) nextToken(position int) (start, end int) {
	if position == 0 {
		if end := s.core(0); end >= 0 {
			return 0, end
		}
	}
	for index := position; index < len(s.text); index++ {
		if !isPrefixChar(s.text[index]) {
			continue
		}
		if end := s.core(index + 1); end >= 0 {
			return index + 1, end
		}
	}
	return -1, -1
}

// quotedCoreAt は `['"]?{SECRET_CORE}` が index から一致するかを返す。
func (s *secretScanner) quotedCoreAt(index int) bool {
	if index >= len(s.text) {
		return false
	}
	if s.core(index) >= 0 {
		return true
	}
	return (s.text[index] == '\'' || s.text[index] == '"') && s.core(index+1) >= 0
}

// skipSpaces は index から空白を読み飛ばした位置を返す。
func (s *secretScanner) skipSpaces(index int) int {
	for index < len(s.text) && py.IsSpace(s.text[index]) {
		index++
	}
	return index
}

// commandAt は index から語 name が始まり、直後が空白で、直前が `(?:^|[\s(])` の境界（allowPath なら / も）かを返す。
// PATH_PREFIX の \S*/ は、直前が / なら必ず境界（先頭か空白か ( ）まで遡れるので、「直前が / 」と同じになる。
func (s *secretScanner) commandAt(index int, name string, allowPath bool) bool {
	if !s.hasAt(index, name) || index+len(name) >= len(s.text) || !py.IsSpace(s.text[index+len(name)]) {
		return false
	}
	if index == 0 {
		return true
	}
	before := s.text[index-1]
	return py.IsSpace(before) || before == '(' || (allowPath && before == '/')
}

// argumentReaches は、コマンド名の直後の空白から、ハイフンで始まる引数の並びを飛ばした先の引数のどれかが
// `['"]?{SECRET_CORE}` に一致するかを返す。minDash はハイフンの後ろに要る \S の数（-\S+ なら 1、-\S* なら 0）である。
// 空白の途中から照合を始めてもトークンは一致しないので、各引数の先頭だけを試せばよい。
func (s *secretScanner) argumentReaches(index, minDash int) bool {
	for {
		index = s.skipSpaces(index)
		if s.quotedCoreAt(index) {
			return true
		}
		if index >= len(s.text) || s.text[index] != '-' {
			return false
		}
		end := index + 1
		for end < len(s.text) && !py.IsSpace(s.text[end]) {
			end++
		}
		// ハイフンの引数の後ろには空白が要る。
		if end-index-1 < minDash || end >= len(s.text) {
			return false
		}
		index = end
	}
}

// movesSecret は `(?:^|[\s(]){PATH_PREFIX}mv\s+(?:-\S+\s+)*['"]?{SECRET_CORE}` である。
func (s *secretScanner) movesSecret() bool {
	for index := range s.text {
		if s.commandAt(index, "mv", true) && s.argumentReaches(index+len("mv"), 1) {
			return true
		}
	}
	return false
}

// teesToSecret は `(?:^|[\s(])tee\s+(?:-\S*\s+)*['"]?{SECRET_CORE}` である。
func (s *secretScanner) teesToSecret() bool {
	for index := range s.text {
		if s.commandAt(index, "tee", false) && s.argumentReaches(index+len("tee"), 0) {
			return true
		}
	}
	return false
}

// redirectsToSecret は `(?<!>)>\s*['"]?{SECRET_CORE}`（>> を除く上書きのリダイレクト）である。
func (s *secretScanner) redirectsToSecret() bool {
	for index, r := range s.text {
		if r != '>' || (index > 0 && s.text[index-1] == '>') {
			continue
		}
		if s.quotedCoreAt(s.skipSpaces(index + 1)) {
			return true
		}
	}
	return false
}

// --- ファイルへ書き出すだけの here-document ------------------------------------------

// 移植元との違い: 移植元は here-document の本文もコマンドとして判定していた。cat > x.sh <<'EOF' ... EOF で
// 書き出すだけの本文はこのコマンドの中では実行されないのに、本文にある秘密ファイルへのリダイレクトなどで止まっていた。
// 次の条件をすべて満たす本文だけを判定から外す。満たさなければ従来どおり本文も判定する。
//   - 本文を受けるのが、ファイルへ書き出すだけの cat（> か >> の先がある）か tee（ファイルの引数がある）である。
//   - 区切りの語がクォートされているか、本文が $(...) もバッククォートも含まない（外側のシェルが本文の中で実行しない）。
//   - 書き出すファイルの名前（パスの最後の要素）が、本文の外にもう一度現れない（同じコマンドで実行する形ではない）。
//   - 区切りの行が見つかり、引用がすべて閉じている（本文の範囲を取り違えて、後ろのコマンドまで外さない）。
//
// 書き出し先そのもの（cat > ~/.ssh/config <<'EOF' の ~/.ssh/config）は本文の外に残るので、従来どおり判定する。
// discard-guard にも同じ考えの処理があるが、hook ごとに誤爆の条件を調整するため共通化しない（AGENTS.md）。

// writtenHeredoc は 1 つの here-document の演算子である。
type writtenHeredoc struct {
	delimiter string
	quoted    bool
	stripTabs bool
	// files は本文を書き出すファイルの名前（パスの最後の要素）である。ファイルへ書き出すだけの形でなければ空である。
	files []string
}

// stripWrittenHeredocs は、ファイルへ書き出すだけの here-document の本文を command から取り除く。
func stripWrittenHeredocs(command string) string {
	if !strings.Contains(command, "<<") {
		return command
	}
	lines := strings.SplitAfter(command, "\n")
	type body struct {
		operator writtenHeredoc
		start    int // 本文の最初の行
		end      int // 区切りの行（本文の外）
	}
	var bodies []body
	var quote byte
	for index := 0; index < len(lines); index++ {
		operators := heredocOperators(lines[index], &quote)
		next := index + 1
		for _, operator := range operators {
			end := next
			for end < len(lines) && !isDelimiterLine(lines[end], operator) {
				end++
			}
			if end >= len(lines) {
				// 区切りの行が無い。本文の範囲を確かめられないので何も外さない。
				return command
			}
			bodies = append(bodies, body{operator, next, end})
			next = end + 1
		}
		index = next - 1
	}
	if quote != 0 || len(bodies) == 0 {
		return command
	}
	inBody := make([]bool, len(lines))
	for _, b := range bodies {
		for line := b.start; line < b.end; line++ {
			inBody[line] = true
		}
	}
	var outside strings.Builder
	for index, line := range lines {
		if !inBody[index] {
			outside.WriteString(line)
		}
	}
	remove := make([]bool, len(lines))
	for _, b := range bodies {
		text := strings.Join(lines[b.start:b.end], "")
		if len(b.operator.files) == 0 ||
			!b.operator.quoted && (strings.Contains(text, "$(") || strings.Contains(text, "`")) ||
			slices.ContainsFunc(b.operator.files, func(name string) bool {
				return strings.Count(outside.String(), name) > 1
			}) {
			continue
		}
		for line := b.start; line < b.end; line++ {
			remove[line] = true
		}
	}
	var result strings.Builder
	for index, line := range lines {
		if !remove[index] {
			result.WriteString(line)
		}
	}
	return result.String()
}

// isDelimiterLine は line が operator の区切りの行かを返す。
func isDelimiterLine(line string, operator writtenHeredoc) bool {
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if operator.stripTabs {
		line = strings.TrimLeft(line, "\t")
	}
	return line == operator.delimiter
}

// heredocOperators は line の中の、クォートの外にある here-document の演算子を出現順に返す。
// quote は行をまたぐクォートの状態である（本文の行では呼ばない）。
func heredocOperators(line string, quote *byte) []writtenHeredoc {
	var operators []writtenHeredoc
	segmentStart := 0
	for index := 0; index < len(line); index++ {
		if next, quoted := skipQuoted(line, index, quote); quoted {
			index = next
			continue
		}
		char := line[index]
		switch char {
		case '\\':
			index++
		case '\'', '"':
			*quote = char
		case ';', '&', '|', '(', ')':
			segmentStart = index + 1
		case '<':
			if !strings.HasPrefix(line[index:], "<<") || strings.HasPrefix(line[index:], "<<<") {
				continue
			}
			operator, end, ok := parseWrittenHeredoc(line, index)
			if !ok {
				continue
			}
			operator.files = writtenFiles(line[segmentStart:index] + " " + line[end:segmentEnd(line, end)])
			operators = append(operators, operator)
			index = end - 1
		}
	}
	return operators
}

// segmentEnd は start から、クォートの外の次のコマンドの区切りまでの終わりの位置を返す。
func segmentEnd(line string, start int) int {
	var quote byte
	for index := start; index < len(line); index++ {
		if next, quoted := skipQuoted(line, index, &quote); quoted {
			index = next
			continue
		}
		switch char := line[index]; char {
		case '\\':
			index++
		case '\'', '"':
			quote = char
		case ';', '&', '|', '(', ')', '\n':
			return index
		}
	}
	return len(line)
}

// skipQuoted は、クォートの中なら line[index] を読んでクォートの状態を進め、最後に読んだ位置と真を返す。
// クォートの外なら index と偽を返す。
func skipQuoted(line string, index int, quote *byte) (int, bool) {
	switch *quote {
	case '\'':
		if line[index] == '\'' {
			*quote = 0
		}
	case '"':
		switch line[index] {
		case '\\':
			index++
		case '"':
			*quote = 0
		}
	default:
		return index, false
	}
	return index, true
}

// parseWrittenHeredoc は line[start:] の `<<` から演算子と区切りの語を読み、演算子の直後の位置を返す。
func parseWrittenHeredoc(line string, start int) (operator writtenHeredoc, end int, ok bool) {
	index := start + 2
	if index < len(line) && line[index] == '-' {
		operator.stripTabs = true
		index++
	}
	for index < len(line) && (line[index] == ' ' || line[index] == '\t') {
		index++
	}
	var word strings.Builder
	for index < len(line) {
		char := line[index]
		switch {
		case char == '\'' || char == '"':
			closing := strings.IndexByte(line[index+1:], char)
			if closing < 0 {
				return writtenHeredoc{}, 0, false
			}
			word.WriteString(line[index+1 : index+1+closing])
			operator.quoted = true
			index += closing + 2
			continue
		case char == '\\':
			operator.quoted = true
			index++
			if index < len(line) {
				word.WriteByte(line[index])
				index++
			}
			continue
		case strings.IndexByte(" \t\r\n;&|<>()", char) >= 0:
		default:
			word.WriteByte(char)
			index++
			continue
		}
		break
	}
	if word.Len() == 0 {
		return writtenHeredoc{}, 0, false
	}
	operator.delimiter = word.String()
	return operator, index, true
}

// writtenFiles は here-document を受けるコマンド（演算子を除いた断片）が、本文をファイルへ書き出すだけの
// cat か tee なら、書き出すファイルの名前（パスの最後の要素）を返す。それ以外は空を返す。
func writtenFiles(segment string) []string {
	tokens, err := py.ShlexSplit(segment, " \t\r\n", "")
	if err != nil || len(tokens) == 0 {
		return nil
	}
	name := tokens[0]
	if slash := strings.LastIndexByte(name, '/'); slash >= 0 {
		name = name[slash+1:]
	}
	var files []string
	switch name {
	case "cat":
		for index := 1; index < len(tokens); index++ {
			token := tokens[index]
			path := ""
			switch {
			case token == ">" || token == ">>":
				if index+1 < len(tokens) {
					path = tokens[index+1]
					index++
				}
			case strings.HasPrefix(token, ">>"):
				path = token[2:]
			case strings.HasPrefix(token, ">"):
				path = token[1:]
			}
			if path != "" {
				files = append(files, path)
			}
		}
	case "tee":
		for _, token := range tokens[1:] {
			if !strings.HasPrefix(token, "-") && !strings.HasPrefix(token, ">") {
				files = append(files, token)
			}
		}
	}
	for index, path := range files {
		files[index] = path[strings.LastIndexByte(path, '/')+1:]
	}
	return slices.DeleteFunc(files, func(name string) bool { return name == "" })
}
