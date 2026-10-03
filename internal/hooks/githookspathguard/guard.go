// Package githookspathguard は core.hooksPath の書き換えと、Git 設定ファイルの直接編集を PreToolUse で拒否する hook
// （git-hookspath-guard）である。
//
// core.hooksPath を書き換えると、global の hook から local の hook へ委譲する仕組みが壊れる。
// 値の設定と削除だけを止め、読み取り（git config core.hooksPath / --get / --list / grep など）はすべて通す。
// Edit / Write は内容ではなく対象のパスで判定する。全体の置き換えでは、新しい内容に core.hooksPath が無くても既存の設定を消せるためである。
// Bash では、同じパスへの明白なリダイレクト・置換・削除を止める。
package githookspathguard

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/HappyOnigiri/hhx/internal/hookrt"
	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

// Name は hook の名前である。
const Name = "git-hookspath-guard"

// Definition は git-hookspath-guard の定義を返す。
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

const key = "core.hookspath"

// pathGateMarkers は一次ゲートで探す Git 設定ファイルのパスの断片である。
// バックスラッシュの表記は JSON の中でエスケープされた形（\\）で探す。
var pathGateMarkers = []string{
	".git/config", `.git\\config`, ".git/worktrees/", `.git\\worktrees\\`, ".git/modules/", `.git\\modules\\`,
}

func gate(input []byte) bool {
	lower := strings.ToLower(string(input))
	if strings.Contains(lower, key) {
		return true
	}
	for _, marker := range pathGateMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func run(c *hookrt.Context) error {
	var toolName string
	var toolInput map[string]any
	if c.FromArgs {
		toolName, toolInput = "Bash", map[string]any{"command": string(c.Input)}
	} else {
		toolName, toolInput = payloadFrom(c.Input)
	}
	// file_path で判定するのは Edit と Write だけである。MultiEdit・NotebookEdit・apply_patch は、
	// command があれば Bash と同じに解析し、無ければ通す（移植元と同じ）。
	if toolName == "Edit" || toolName == "Write" {
		if path, _ := toolInput["file_path"].(string); isGitConfigPath(path) {
			c.Deny(messages.T(c.Language(), idReason))
		}
		return nil
	}
	// 文字列でない command は、移植元では判定の途中で例外になり無出力で終わっていた。
	command, _ := toolInput["command"].(string)
	if command == "" {
		return nil
	}
	for _, segment := range separatorRE.Split(command, -1) {
		if isWrite(segment) || isDirectConfigWrite(segment) {
			c.Deny(messages.T(c.Language(), idReason))
			return nil
		}
	}
	return nil
}

// payloadFrom は payload から tool_name と tool_input を取り出す。どちらも型が違えば空として扱う。
func payloadFrom(raw []byte) (string, map[string]any) {
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return "", nil
	}
	toolInput, ok := payload["tool_input"].(map[string]any)
	if !ok {
		return "", nil
	}
	toolName, _ := payload["tool_name"].(string)
	return toolName, toolInput
}

var writeFlags = map[string]bool{"--unset": true, "--unset-all": true, "--replace-all": true, "--add": true}

var (
	// シェルのコマンド区切り。改行も区切りとして扱う（空白に潰すと「読み取り; 書き込み」が 1 つに混ざり、書き込みを取り逃す）。
	separatorRE = regexp.MustCompile(`\|\||&&|[;|&\n]`)
	// GIT_CONFIG_COUNT / KEY_n / VALUE_n による設定は git config を経由しない。
	envKeyRE = regexp.MustCompile(`^git_config_key_` + py.Digit + `+=core\.hookspath$`)
	// 通常のリポジトリ、bare repository、linked worktree、submodule の設定ファイルを含む。
	// 移植元（Python）の $ は末尾の改行の直前にも一致するので、\n? で同じ意味にする。
	gitConfigPathRE = regexp.MustCompile(`(?i)(?:^|/)(?:\.git(?:/[^/]+)*|[^/]+\.git)/config(?:\.worktree)?\n?$`)
	// 対象のパスと同じセグメントにあれば止める変更コマンド。
	directWriteCommandRE = regexp.MustCompile(`(?i)(?:^|[;&|]|` + py.Space + `)(?:` + py.NotSpace +
		`*/)?(?:tee|truncate|rm|unlink|vi|vim|nano|ed)(?:` + py.Space + `|$)`)
	// sed / perl のその場編集（-i を含むフラグ）。移植元の先読み (?=\s|$) は RE2 で使えないので、
	// 「コマンド名の直後が空白」を 2 通りの形に展開する。
	// 直後の空白がそのままフラグの前の空白になる形と、改行以外の空白の後に任意の引数が続く形である。
	inPlaceEditRE = regexp.MustCompile(`(?i)(?:^|[;&|]|` + py.Space + `)(?:` + py.NotSpace + `*/)?(?:sed|perl)(?:` +
		py.Space + `|` + py.SpaceExceptNewline + `[^;&|\n]*` + py.Space + `)-[A-Za-z]*i[A-Za-z]*(?:` + py.Space + `|$)`)
	pathPunctuationRE = regexp.MustCompile("[\"'`(){}\\[\\],]")
	leadingRedirectRE = regexp.MustCompile(`^` + py.Digit + `*[<>]+`)
	emptyValueRE      = regexp.MustCompile(`""|''`)
	writePunctuation  = regexp.MustCompile("[\"'`(){}\\[\\]]")
	redirectRE        = regexp.MustCompile(`[0-9]*[<>]+&?` + py.Space + `*` + py.NotSpace + `*`)
)

// isGitConfigPath は、直接の編集を禁じるリポジトリの Git 設定ファイルかを返す。
func isGitConfigPath(path string) bool {
	if path == "" {
		return false
	}
	normalized := strings.TrimRight(strings.ReplaceAll(path, `\`, "/"), "/")
	return gitConfigPathRE.MatchString(normalized)
}

// mentionedGitConfigPaths は Bash のセグメントに字句として現れる Git 設定ファイルのパスを返す。
func mentionedGitConfigPaths(segment string) []string {
	normalized := strings.ReplaceAll(segment, `\`, "/")
	var paths []string
	for _, token := range py.Fields(pathPunctuationRE.ReplaceAllString(normalized, " ")) {
		token = leadingRedirectRE.ReplaceAllString(token, "")
		value := token
		if index := strings.LastIndex(token, "="); index >= 0 {
			value = token[index+1:]
		}
		for _, candidate := range []string{token, value} {
			if isGitConfigPath(candidate) {
				paths = append(paths, candidate)
			}
		}
	}
	return paths
}

// isDirectConfigWrite は Bash のセグメントが Git 設定ファイルを直接書き換える形かを返す。
func isDirectConfigWrite(segment string) bool {
	paths := mentionedGitConfigPaths(segment)
	if len(paths) == 0 {
		return false
	}
	// 対象のファイルへの > / >>。読み取った内容を別のファイルへ出す形は通す。
	normalized := strings.ReplaceAll(segment, `\`, "/")
	for _, path := range paths {
		redirect := regexp.MustCompile(py.Digit + `*>{1,2}` + py.Space + `*["']?` + regexp.QuoteMeta(path) +
			`(?:["']|` + py.Space + `|$)`)
		if redirect.MatchString(normalized) {
			return true
		}
	}
	// cp / mv / install は最後の引数が書き込み先である。設定ファイルからバックアップする向きは通す。
	tokens := py.Fields(pathPunctuationRE.ReplaceAllString(normalized, " "))
	for index, token := range tokens {
		switch strings.ToLower(basename(token)) {
		case "cp", "mv", "install":
			var operands []string
			for _, item := range tokens[index+1:] {
				if !strings.HasPrefix(item, "-") {
					operands = append(operands, item)
				}
			}
			if len(operands) > 0 && isGitConfigPath(operands[len(operands)-1]) {
				return true
			}
		}
	}
	// dd は of= が書き込み先である。if=.git/config による読み取りは通す。
	if hasDD(tokens) {
		for _, token := range tokens {
			if strings.HasPrefix(strings.ToLower(token), "of=") && isGitConfigPath(token[3:]) {
				return true
			}
		}
	}
	// 引数を完全には解析せず、対象のパスと既知の変更コマンドが同じセグメントにあれば止める。
	return directWriteCommandRE.MatchString(segment) || inPlaceEditRE.MatchString(segment)
}

func hasDD(tokens []string) bool {
	for _, token := range tokens {
		if strings.ToLower(basename(token)) == "dd" {
			return true
		}
	}
	return false
}

// basename は最後の / より後ろを返す（Python の token.rsplit("/", 1)[-1]）。
func basename(token string) string {
	return token[strings.LastIndex(token, "/")+1:]
}

// isWrite は 1 セグメントが core.hooksPath への書き込みかを返す。
func isWrite(segment string) bool {
	if !strings.Contains(strings.ToLower(segment), key) {
		return false
	}
	// 空文字列の代入（core.hooksPath ""）を値のトークンとして残す。
	cleaned := emptyValueRE.ReplaceAllString(segment, " __emptyvalue__ ")
	// 引用符・バッククォート・括弧を空白にする。[ ] も落とす:
	// [ -z "$(git config core.hooksPath)" ] の閉じ括弧を値のトークンと誤認しないため。
	cleaned = writePunctuation.ReplaceAllString(cleaned, " ")
	// リダイレクト（2>/dev/null、> out、>&2 など）を対象ごと除く。
	cleaned = redirectRE.ReplaceAllString(cleaned, " ")
	tokens := py.Fields(strings.ToLower(cleaned))

	var hasGit, hasConfig, hasWriteFlag, inlineAssign, envAssign bool
	keyIndex := -1
	for index, token := range tokens {
		switch {
		case token == "git" || strings.HasSuffix(token, "/git"):
			hasGit = true
		case token == "config":
			hasConfig = true
		case writeFlags[token]:
			hasWriteFlag = true
		case token == key:
			keyIndex = index
		case strings.HasPrefix(token, key+"="):
			inlineAssign = true
		case envKeyRE.MatchString(token):
			envAssign = true
		}
	}
	switch {
	case envAssign:
		// GIT_CONFIG_KEY_n=core.hooksPath（git config を経由しない設定）。
		return true
	case !hasGit:
		return false
	case inlineAssign:
		// git -c core.hooksPath=<値>
		return true
	case !hasConfig || keyIndex < 0:
		return false
	case hasWriteFlag:
		// --unset / --add / --replace-all によるキーの操作。
		return true
	}
	// キーの直後に値のトークンが続く（フラグは値ではない）。
	if keyIndex+1 >= len(tokens) {
		return false
	}
	return !strings.HasPrefix(tokens[keyIndex+1], "-")
}
