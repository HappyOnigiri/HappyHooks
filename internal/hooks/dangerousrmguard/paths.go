package dangerousrmguard

import (
	"errors"
	"regexp"
	"strings"

	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

// --- Claude Code v2.1.239 からの移植 ---------------------------------------
//
// Python の \s・\S・\d は pycompat の文字クラスで書き、`\Z` は RE2 の `\z` にする。
// 先読み・後読みを使っていたもの（PAREN・BACKGROUND_AMP・KNOWN_VAR・PRIVATE_ALIAS）は、同じ置き換えをする走査関数にした。

const (
	// maxSubstitutions は組み込みが 1 コマンドあたりに許すコマンド置換の数である。
	maxSubstitutions = 64
	// cmdsubToken は実体パス判定（V7r 系）でコマンド置換を置き換えるトークンである。組み込みの g9v と同じ形。
	cmdsubToken = "__CMDSUB__"
	// assignments は先行する VAR=... 代入の並びである。
	assignments = `(?:[A-Za-z_][A-Za-z0-9_]*\+?=` + py.NotSpace + `*` + py.Space + `+)*`
)

var (
	// l6vRE はコマンド断片の先頭が rm / rmdir か（先行する VAR=... 代入とパス指定つき起動を許す）である。
	l6vRE = regexp.MustCompile(`^` + assignments + `\\?(?:[^=` + py.SpaceChars + `]*/)?(rm|rmdir)(?:` + py.Space + `|\z)`)
	// a6vRE は削除対象が `$VAR/` で始まり、直後が * $ / クォート 末尾 のいずれかかである。
	a6vRE = regexp.MustCompile(`^"?\$(?:\{[A-Za-z_][A-Za-z0-9_]*\}|[A-Za-z_][A-Za-z0-9_]*)"?/(?:\*|\$|/|["']|\z)`)
	// Claude Code v2.1.283 の e$t: 代入の右辺が空変数からルート直下へ広がる形。
	// 実装で使わない \uE020（引用済み区切りの番兵）は除いた。
	derivedRootRE = regexp.MustCompile(`^["']*\$(?:\{[A-Za-z_][A-Za-z0-9_]*` +
		`(?::?-(?:["']{2}|"?\$\{?[A-Za-z_][A-Za-z0-9_]*\}?"?)?)?\}|[A-Za-z_][A-Za-z0-9_]*)` +
		`["']*\\?/(?:[*?[{]|\$|/|["']|\z)`)
	// 同版の i$t: 単独の変数展開を削除対象にした形。
	bareVariableRE = regexp.MustCompile(`^["']*\$(?:\{([A-Za-z_][A-Za-z0-9_]*)` +
		`(?::?[?-][^}]*)?\}|([A-Za-z_][A-Za-z0-9_]*))["']*\z`)
	assignmentRE = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=([^` + py.SpaceChars + `;&|]+)(?:` + py.Space + `|\z)`)

	lineContinuationRE = regexp.MustCompile(`\\\r?\n`)
	backtickRE         = regexp.MustCompile("`([^`]*)`")
	segmentRE          = regexp.MustCompile(`[;|\n\r]|&&`)
	trailingClosersRE  = regexp.MustCompile(`[)\]}]+\z`)
	redirectHeadRE     = regexp.MustCompile(`^[` + py.Digit + `&]*[<>]`)
	redirectOnlyRE     = regexp.MustCompile(`^(?:[0-9]+|&)?(?:>>?[|&]?|<<?<?|<>)\z`)
	// rmWordRE は `\brm(?:dir)?\b` である。両端の \b は語の文字 r・m（r）の外側にあるので、1 文字を消費する形で書ける。
	rmWordRE = regexp.MustCompile(py.NotWordOrStart + `rm(?:dir)?` + py.NotWordOrEnd)
	// leadingKeywordsRE は断片の先頭に続く予約語の並びである。`do ! rm` のように重なる形も外す。
	leadingKeywordsRE = regexp.MustCompile(`^(?:(?:do|then|else|elif|if|while|until|coproc|!|\{)` + py.Space + `+)+`)
	cdWordRE          = regexp.MustCompile(`^` + assignments + `(?:cd|chdir|pushd|popd)(?:` + py.Space + `|\z)`)
	globCharE         = regexp.MustCompile(`[*?\[]`)
	// Python の $ は末尾の改行の直前にも一致するので、\n?\z で同じ意味にする。
	globTailRE      = regexp.MustCompile(`(?:/\*+)+/*(\n?)\z`)
	dotdotRE        = regexp.MustCompile(`(?:^|/)\.\.(?:/|\n?\z)`)
	trailingGlobDir = regexp.MustCompile(`\*/+\n?\z`)
	rmdirParentsRE  = regexp.MustCompile(`^--p|^-[a-z]*p`)
	privateAliasRE  = regexp.MustCompile(`(?i)^/private/(etc|var|tmp|home)`)
	slashesRE       = regexp.MustCompile(`/+`)
	whitespaceRE    = regexp.MustCompile(py.Space + `+`)
)

// alias は macOS の /private/{etc,var,tmp,home} を実効パスへ寄せる（組み込みの n() 相当）。
// 移植元の `(/|$)` の $ は末尾の改行の直前にも一致するので、直後が空・/・改行 1 文字のどれかなら寄せる。
func alias(path string) string {
	match := privateAliasRE.FindStringSubmatchIndex(path)
	if match == nil {
		return path
	}
	rest := path[match[1]:]
	if rest != "" && rest != "\n" && !strings.HasPrefix(rest, "/") {
		return path
	}
	return "/" + path[match[2]:match[3]] + rest
}

// realpath は移植元の _realpath である。OSError にあたる失敗は元のパスを返し、NUL を含むパス（ValueError）はエラーにする。
func realpath(path string) (string, error) {
	resolved, err := py.Realpath(path)
	if err == nil {
		return resolved, nil
	}
	if errors.Is(err, py.ErrNUL) {
		return "", err
	}
	return path, nil
}

// trimTrailingSlash は "/" 以外の末尾の / を 1 つ除く。
func trimTrailingSlash(path string) string {
	if path != "/" && strings.HasSuffix(path, "/") {
		return path[:len(path)-1]
	}
	return path
}

// isCriticalPath は組み込みの Kar() の移植である。ルート・最上位ディレクトリ・ホームディレクトリかを返す。
func isCriticalPath(path string) (bool, error) {
	collapsed := slashesRE.ReplaceAllString(path, "/")
	if collapsed == "*" || strings.HasSuffix(collapsed, "/*") {
		return true, nil
	}
	target := trimTrailingSlash(alias(collapsed))
	if target == "/" {
		return true, nil
	}
	home := trimTrailingSlash(alias(slashesRE.ReplaceAllString(py.Home(), "/")))
	if py.Lower(target) == py.Lower(home) {
		return true, nil
	}
	resolvedHome, err := realpath(py.Home())
	if err != nil {
		return false, err
	}
	// 移植元と異なり、ホームの実体にも対象と同じ別名処理を適用し、/private 経由の削除を見逃さない。
	canonical := trimTrailingSlash(alias(slashesRE.ReplaceAllString(resolvedHome, "/")))
	if canonical != home && py.Lower(target) == py.Lower(canonical) {
		return true, nil
	}
	return py.Dirname(target) == "/", nil
}

// removesWorkspace は組み込みの MF() の移植である。target が base と一致するか、base の祖先かを返す。
func removesWorkspace(base, target string) (bool, error) {
	relative, err := py.Relpath(py.Lower(alias(py.Normpath(base))), py.Lower(alias(py.Normpath(target))))
	if errors.Is(err, py.ErrEmptyPath) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch {
	case relative == ".":
		return true, nil
	case relative == ".." || strings.HasPrefix(relative, "../"):
		return false, nil
	default:
		return !py.IsAbs(relative), nil
	}
}

// escapesUpward は組み込みの F5n() の移植である。実体の要素より後ろに `..` が現れるかを返す。
func escapesUpward(path string) bool {
	seenReal := false
	for _, component := range strings.Split(path, "/") {
		switch component {
		case "", ".":
		case "..":
			if seenReal {
				return true
			}
		default:
			seenReal = true
		}
	}
	return false
}

// stripGlobTail は末尾の `/*` `/**` `/*/` … を取り除く（組み込みの for ループの移植）。
func stripGlobTail(path string) string {
	current := path
	for {
		previous := current
		stripped := globTailRE.ReplaceAllString(current, "${1}")
		if stripped == "" {
			stripped = "/"
		}
		if stripped != current {
			current = stripped
			if strings.Contains(stripped, "/") {
				current = py.Normpath(stripped)
			}
		}
		if previous == current {
			return current
		}
	}
}
