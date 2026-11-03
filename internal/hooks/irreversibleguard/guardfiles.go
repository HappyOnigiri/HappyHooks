package irreversibleguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

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
